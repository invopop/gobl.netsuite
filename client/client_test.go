package client_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/invopop/gobl.netsuite/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testAuth = &client.TBA{
	ConsumerKey:    "ck",
	ConsumerSecret: "cs",
	TokenID:        "tk",
	TokenSecret:    "ts",
}

func newTestClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	nc, err := client.New("1234567-sb1", testAuth, client.WithBaseURL(srv.URL))
	require.NoError(t, err)
	return nc
}

func TestNew(t *testing.T) {
	_, err := client.New("", testAuth)
	assert.ErrorContains(t, err, "account ID is required")
	_, err = client.New("123", nil)
	assert.ErrorContains(t, err, "auth is required")
}

func TestAccountForms(t *testing.T) {
	assert.Equal(t, "1234567-sb1", client.AccountHost("1234567_SB1"))
	assert.Equal(t, "1234567_SB1", client.AccountRealm("1234567-sb1"))
}

func TestGetRecord(t *testing.T) {
	nc := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/services/rest/record/v1/invoice/42", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("expandSubResources"))
		auth := r.Header.Get("Authorization")
		assert.True(t, strings.HasPrefix(auth, "OAuth "))
		assert.Contains(t, auth, `realm="1234567_SB1"`)
		assert.Contains(t, auth, `oauth_signature_method="HMAC-SHA256"`)
		assert.Contains(t, auth, `oauth_token="tk"`)
		_, _ = w.Write([]byte(`{"id":"42","tranId":"INV-1"}`))
	})

	out := struct {
		ID     string `json:"id"`
		TranID string `json:"tranId"`
	}{}
	require.NoError(t, nc.GetRecord(t.Context(), "invoice", "42", true, &out))
	assert.Equal(t, "INV-1", out.TranID)

	var raw json.RawMessage
	require.NoError(t, nc.GetRecord(t.Context(), "invoice", "42", true, &raw))
	assert.JSONEq(t, `{"id":"42","tranId":"INV-1"}`, string(raw))
}

func TestQuery(t *testing.T) {
	nc := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/services/rest/query/v1/suiteql", r.URL.Path)
		assert.Equal(t, "5", r.URL.Query().Get("limit"))
		assert.Equal(t, "transient", r.Header.Get("Prefer"))
		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"q":"SELECT id FROM transaction"}`, string(body))
		_, _ = w.Write([]byte(`{"count":1,"hasMore":false,"offset":0,"totalResults":1,"items":[{"id":"42"}]}`))
	})

	res, err := nc.Query(t.Context(), "SELECT id FROM transaction", 5, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Count)
	require.Len(t, res.Items, 1)
	assert.JSONEq(t, `{"id":"42"}`, string(res.Items[0]))
}

func TestCreateRecord(t *testing.T) {
	nc := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/services/rest/record/v1/customer", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"companyName":"ACME"}`, string(body))
		w.Header().Set("Location", "https://1234567-sb1.suitetalk.api.netsuite.com/services/rest/record/v1/customer/99")
		w.WriteHeader(http.StatusNoContent)
	})
	id, err := nc.CreateRecord(t.Context(), "customer", map[string]string{"companyName": "ACME"})
	require.NoError(t, err)
	assert.Equal(t, "99", id)
}

func TestErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		nc := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"https://www.rfc-editor.org/rfc/rfc9110.html#section-15.5.5","title":"Not Found","status":404,"o:errorDetails":[{"detail":"The record instance does not exist.","o:errorCode":"NONEXISTENT_ID"}]}`))
		})
		err := nc.GetRecord(t.Context(), "invoice", "1", false, nil)
		var nsErr *client.Error
		require.ErrorAs(t, err, &nsErr)
		assert.True(t, nsErr.NotFound())
		assert.False(t, nsErr.Retryable())
		assert.Equal(t, "netsuite: 404 Not Found: [NONEXISTENT_ID] The record instance does not exist.", err.Error())
	})

	t.Run("retryable", func(t *testing.T) {
		nc := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"o:errorDetails":[{"detail":"limit","o:errorCode":"CONCURRENCY_LIMIT_EXCEEDED"}]}`))
		})
		err := nc.GetRecord(t.Context(), "invoice", "1", false, nil)
		var nsErr *client.Error
		require.ErrorAs(t, err, &nsErr)
		assert.True(t, nsErr.Retryable())
	})

	t.Run("unparseable body", func(t *testing.T) {
		nc := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`nope`))
		})
		err := nc.GetRecord(t.Context(), "invoice", "1", false, nil)
		assert.EqualError(t, err, "netsuite: 401: nope")
	})
}
