package netsuite_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl.netsuite/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeNetSuite serves records by path, counting requests and how many run
// at once.
type fakeNetSuite struct {
	records map[string]string
	delay   time.Duration

	mu      sync.Mutex
	calls   map[string]int
	active  int
	maxSeen int
}

func (f *fakeNetSuite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls[r.URL.Path]++
	f.active++
	f.maxSeen = max(f.maxSeen, f.active)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	time.Sleep(f.delay)
	body, ok := f.records[strings.TrimPrefix(r.URL.Path, "/services/rest/record/v1/")]
	if !ok {
		http.Error(w, `{"title":"Not Found"}`, http.StatusNotFound)
		return
	}
	_, _ = w.Write([]byte(body))
}

func newFakeNetSuite(t *testing.T, records map[string]string) (*fakeNetSuite, *client.Client) {
	t.Helper()
	f := &fakeNetSuite{records: records, delay: 20 * time.Millisecond, calls: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	auth := &client.TBA{ConsumerKey: "ck", ConsumerSecret: "cs", TokenID: "tk", TokenSecret: "ts"}
	nc, err := client.New("1234567", auth, client.WithBaseURL(srv.URL))
	require.NoError(t, err)
	return f, nc
}

func fetchRecords() map[string]string {
	return map[string]string{
		"invoice/1": `{"id":"1","entity":{"id":"10"},"subsidiary":{"id":"20"},"currency":{"id":"1"},
			"item":{"items":[{"line":1,"taxCode":{"id":"5"}},{"line":2,"taxCode":{"id":"6"}},{"line":3,"taxCode":{"id":"5"}}]}}`,
		"customer/10":    `{"id":"10","companyName":"Acme"}`,
		"subsidiary/20":  `{"id":"20","currency":{"id":"2"}}`,
		"currency/1":     `{"id":"1","symbol":"EUR"}`,
		"currency/2":     `{"id":"2","symbol":"USD"}`,
		"salestaxitem/5": `{"id":"5","itemId":"S-DE"}`,
		"salestaxitem/6": `{"id":"6","itemId":"Z-DE"}`,
	}
}

func TestFetchInvoiceRelatedRecords(t *testing.T) {
	f, nc := newFakeNetSuite(t, fetchRecords())

	b, err := netsuite.FetchInvoice(t.Context(), nc, "1")
	require.NoError(t, err)

	assert.JSONEq(t, `{"id":"10","companyName":"Acme"}`, string(b.Customer))
	assert.JSONEq(t, `{"id":"20","currency":{"id":"2"}}`, string(b.Subsidiary))
	assert.Len(t, b.Currencies, 2, "the transaction's and the subsidiary's currency")
	assert.Contains(t, b.Currencies, "1")
	assert.Contains(t, b.Currencies, "2")
	assert.Len(t, b.TaxCodes, 2)
	assert.Contains(t, b.TaxCodes, "5")
	assert.Contains(t, b.TaxCodes, "6")

	assert.Equal(t, 1, f.calls["/services/rest/record/v1/salestaxitem/5"], "a tax code used on two lines is fetched once")
	assert.Greater(t, f.maxSeen, 1, "related records are fetched at the same time")
	assert.LessOrEqual(t, f.maxSeen, 3, "at most three requests at once")
}

func TestFetchInvoiceRelatedRecordError(t *testing.T) {
	records := fetchRecords()
	delete(records, "salestaxitem/6")
	_, nc := newFakeNetSuite(t, records)

	_, err := netsuite.FetchInvoice(t.Context(), nc, "1")
	assert.ErrorContains(t, err, "fetching salestaxitem 6")
}
