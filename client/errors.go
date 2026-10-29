package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Error codes returned by NetSuite that indicate a request may succeed if
// retried later.
var retryableCodes = []string{
	"SSS_REQUEST_LIMIT_EXCEEDED",
	"CONCURRENCY_LIMIT_EXCEEDED",
}

// ErrorDetail is a single entry of the error details provided by NetSuite.
type ErrorDetail struct {
	Detail    string `json:"detail"`
	ErrorCode string `json:"o:errorCode"`
	ErrorPath string `json:"o:errorPath,omitempty"`
}

// Error is returned when NetSuite responds with an error status.
type Error struct {
	StatusCode int           `json:"status"`
	Title      string        `json:"title"`
	Details    []ErrorDetail `json:"o:errorDetails"`
	Body       string        `json:"-"`
}

func newError(resp *http.Response, body []byte) *Error {
	e := new(Error)
	_ = json.Unmarshal(body, e) // best effort, keep raw body regardless
	e.StatusCode = resp.StatusCode
	e.Body = string(body)
	return e
}

// Error implements the error interface.
func (e *Error) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "netsuite: %d", e.StatusCode)
	if e.Title != "" {
		fmt.Fprintf(&sb, " %s", e.Title)
	}
	for _, d := range e.Details {
		fmt.Fprintf(&sb, ": [%s] %s", d.ErrorCode, d.Detail)
	}
	if e.Title == "" && len(e.Details) == 0 && e.Body != "" {
		fmt.Fprintf(&sb, ": %s", e.Body)
	}
	return sb.String()
}

// Retryable returns true when the error is likely to be temporary.
func (e *Error) Retryable() bool {
	if e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError {
		return true
	}
	for _, d := range e.Details {
		if slices.Contains(retryableCodes, d.ErrorCode) {
			return true
		}
	}
	return false
}

// NotFound returns true when the requested resource does not exist.
func (e *Error) NotFound() bool {
	return e.StatusCode == http.StatusNotFound
}
