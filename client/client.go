// Package client provides a minimal client for the NetSuite REST web services
// (SuiteTalk REST), covering the record and SuiteQL query APIs.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// defaultHostPattern is the REST web services host for an account, where
	// the placeholder is the account ID in its URL form (lowercase, hyphens).
	defaultHostPattern = "https://%s.suitetalk.api.netsuite.com"

	recordPath = "/services/rest/record/v1"
	queryPath  = "/services/rest/query/v1/suiteql"
)

// Client makes authenticated requests to the NetSuite REST web services
// of a single account.
type Client struct {
	accountID string
	baseURL   string
	http      *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the base URL derived from the account ID. Mainly
// useful for tests.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimSuffix(u, "/")
	}
}

// New creates a client for the given account ID using the provided
// authentication method.
func New(accountID string, auth Auth, opts ...Option) (*Client, error) {
	if accountID == "" {
		return nil, fmt.Errorf("account ID is required")
	}
	if auth == nil {
		return nil, fmt.Errorf("auth is required")
	}
	c := &Client{
		accountID: accountID,
		baseURL:   fmt.Sprintf(defaultHostPattern, AccountHost(accountID)),
	}
	for _, opt := range opts {
		opt(c)
	}
	c.http = auth.HTTPClient(context.Background(), accountID)
	return c, nil
}

// AccountHost converts an account ID into the form used in NetSuite
// hostnames, e.g. "1234567_SB1" becomes "1234567-sb1".
func AccountHost(accountID string) string {
	return strings.ToLower(strings.ReplaceAll(accountID, "_", "-"))
}

// AccountRealm converts an account ID into the form used as the OAuth
// realm, e.g. "1234567-sb1" becomes "1234567_SB1".
func AccountRealm(accountID string) string {
	return strings.ToUpper(strings.ReplaceAll(accountID, "-", "_"))
}

// GetRecord fetches a single record by type and internal ID, decoding the
// response into out. When expand is true, sublists and subrecords are
// included in the response instead of links.
func (c *Client) GetRecord(ctx context.Context, recordType, id string, expand bool, out any) error {
	q := url.Values{}
	if expand {
		q.Set("expandSubResources", "true")
	}
	p := fmt.Sprintf("%s/%s/%s", recordPath, url.PathEscape(recordType), url.PathEscape(id))
	return c.Get(ctx, p, q, out)
}

// QueryResult is a single page of results from a SuiteQL query.
type QueryResult struct {
	Count        int               `json:"count"`
	HasMore      bool              `json:"hasMore"`
	Offset       int               `json:"offset"`
	TotalResults int               `json:"totalResults"`
	Items        []json.RawMessage `json:"items"`
}

// Query runs a SuiteQL query and returns a single page of results. A limit
// of zero uses the NetSuite default (1000).
func (c *Client) Query(ctx context.Context, sql string, limit, offset int) (*QueryResult, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprint(offset))
	}
	body, err := json.Marshal(map[string]string{"q": sql})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, queryPath, q, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Prefer", "transient")
	res := new(QueryResult)
	if err := c.do(req, res); err != nil {
		return nil, err
	}
	return res, nil
}

// Get performs a GET request against the given path, relative to the
// account's base URL, and decodes the JSON response into out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return newError(resp, data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = data
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
