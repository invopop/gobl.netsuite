package netsuite

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/invopop/gobl.netsuite/client"
	"github.com/invopop/gobl/l10n"
	"github.com/invopop/gobl/org"
	"golang.org/x/sync/errgroup"
)

// Record types of the transactions supported by the conversion.
const (
	RecordTypeInvoice    = "invoice"
	RecordTypeCreditMemo = "creditmemo"
)

// Transaction types, as found in the type field of transaction records and
// the type column of SuiteQL's transaction table.
const (
	transactionTypeInvoice    = "custinvc"
	transactionTypeCreditMemo = "custcred"
)

// Bundle holds the raw NetSuite records needed to convert a transaction into
// a GOBL document. Records are kept exactly as returned by the REST API so
// that nothing is lost, including custom fields, and the bundle can be
// stored or handed to mappings as the conversion source.
type Bundle struct {
	// Transaction is the invoice or credit memo to convert.
	Transaction json.RawMessage `json:"transaction"`
	Customer    json.RawMessage `json:"customer,omitempty"`
	Subsidiary  json.RawMessage `json:"subsidiary,omitempty"`
	// Currencies are indexed by internal ID.
	Currencies map[string]json.RawMessage `json:"currencies,omitempty"`
	// TaxCodes are indexed by internal ID.
	TaxCodes map[string]json.RawMessage `json:"taxCodes,omitempty"`
	// Related are other transactions referred to, indexed by internal ID,
	// such as the invoices a credit memo was created from or applied to.
	Related map[string]json.RawMessage `json:"related,omitempty"`
}

// FetchInvoice loads an invoice and the related records needed for its
// conversion from NetSuite.
func FetchInvoice(ctx context.Context, nc *client.Client, id string) (*Bundle, error) {
	return fetchTransaction(ctx, nc, RecordTypeInvoice, id)
}

// FetchCreditMemo loads a credit memo and the related records needed for
// its conversion from NetSuite, including the invoices it was created from
// or applied to.
func FetchCreditMemo(ctx context.Context, nc *client.Client, id string) (*Bundle, error) {
	return fetchTransaction(ctx, nc, RecordTypeCreditMemo, id)
}

// fetchConcurrency is how many requests a transaction's related records are
// fetched with at once. NetSuite limits concurrent requests per account,
// shared with the account's other integrations, so it's kept low.
const fetchConcurrency = 3

func fetchTransaction(ctx context.Context, nc *client.Client, recordType, id string) (*Bundle, error) {
	b := &Bundle{
		Currencies: make(map[string]json.RawMessage),
		TaxCodes:   make(map[string]json.RawMessage),
	}
	if err := nc.GetRecord(ctx, recordType, id, true, &b.Transaction); err != nil {
		return nil, fmt.Errorf("fetching %s %s: %w", recordType, id, err)
	}
	tx := new(Transaction)
	if err := json.Unmarshal(b.Transaction, tx); err != nil {
		return nil, fmt.Errorf("parsing %s %s: %w", recordType, id, err)
	}

	// The other records only depend on the transaction, so they're fetched
	// at the same time. Each task writes its own field of the bundle, and
	// the shared indexes are guarded by f.
	f := &fetcher{nc: nc}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fetchConcurrency)

	if tx.Entity != nil {
		g.Go(func() error {
			if err := nc.GetRecord(gctx, "customer", tx.Entity.ID, true, &b.Customer); err != nil {
				return fmt.Errorf("fetching customer %s: %w", tx.Entity.ID, err)
			}
			return nil
		})
	}

	// Both the transaction currency and the subsidiary's base currency are
	// needed to describe exchange rates. The base currency is only known
	// once the subsidiary is loaded, so it's fetched in the same task.
	g.Go(func() error {
		return f.fetchInto(gctx, "currency", tx.Currency, b.Currencies)
	})
	if tx.Subsidiary != nil {
		g.Go(func() error {
			if err := nc.GetRecord(gctx, "subsidiary", tx.Subsidiary.ID, true, &b.Subsidiary); err != nil {
				return fmt.Errorf("fetching subsidiary %s: %w", tx.Subsidiary.ID, err)
			}
			sub := new(Subsidiary)
			if err := json.Unmarshal(b.Subsidiary, sub); err != nil {
				return fmt.Errorf("parsing subsidiary %s: %w", tx.Subsidiary.ID, err)
			}
			return f.fetchInto(gctx, "currency", sub.Currency, b.Currencies)
		})
	}

	// TODO: accounts may also use tax groups (record type "taxgroup") that
	// combine several tax codes, which are not supported yet.
	for _, ref := range taxCodeRefs(tx) {
		g.Go(func() error {
			return f.fetchInto(gctx, "salestaxitem", ref, b.TaxCodes)
		})
	}

	g.Go(func() error {
		return fetchPrecedingInvoices(gctx, f, tx, b)
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return b, nil
}

// taxCodeRefs lists the tax codes of a transaction's lines once each, from
// the lines with legacy tax, and from the tax details with SuiteTax.
func taxCodeRefs(tx *Transaction) []*Ref {
	var refs []*Ref
	add := func(ref *Ref) {
		if ref == nil || ref.ID == "" {
			return
		}
		for _, r := range refs {
			if r.ID == ref.ID {
				return
			}
		}
		refs = append(refs, ref)
	}
	if tx.Item != nil {
		for _, it := range tx.Item.Items {
			add(it.TaxCode)
		}
	}
	if tx.TaxDetails != nil {
		for _, d := range tx.TaxDetails.Items {
			add(d.TaxCode)
		}
	}
	return refs
}

// fetchPrecedingInvoices loads the invoices a transaction was created from
// or applied to. As the created from reference does not say what type of
// transaction it refers to, the types are looked up first so that only
// invoices are loaded, and not for example a return authorization.
func fetchPrecedingInvoices(ctx context.Context, f *fetcher, tx *Transaction, b *Bundle) error {
	ids := precedingIDs(tx)
	if len(ids) == 0 {
		return nil
	}
	// IDs are numeric, but are checked before building the query regardless.
	for _, id := range ids {
		if strings.Trim(id, "0123456789") != "" {
			return fmt.Errorf("invalid transaction ID %q", id)
		}
	}
	res, err := f.nc.Query(ctx, fmt.Sprintf(
		"SELECT id, type FROM transaction WHERE id IN (%s)", strings.Join(ids, ", ")), 0, 0)
	if err != nil {
		return fmt.Errorf("finding preceding transaction types: %w", err)
	}
	b.Related = make(map[string]json.RawMessage)
	for _, item := range res.Items {
		row := struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}{}
		if err := json.Unmarshal(item, &row); err != nil {
			return fmt.Errorf("parsing transaction types: %w", err)
		}
		if !strings.EqualFold(row.Type, transactionTypeInvoice) {
			continue
		}
		if err := f.fetchInto(ctx, RecordTypeInvoice, &Ref{ID: row.ID}, b.Related); err != nil {
			return err
		}
	}
	return nil
}

// precedingIDs lists the IDs of the transactions a transaction was created
// from or applied to, in that order and without duplicates.
func precedingIDs(tx *Transaction) []string {
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if tx.CreatedFrom != nil {
		add(tx.CreatedFrom.ID)
	}
	if tx.Apply != nil {
		for _, a := range tx.Apply.Items {
			if a.Apply && a.Doc != nil {
				add(a.Doc.ID)
			}
		}
	}
	return ids
}

// fetcher loads records into a bundle's indexes, from several goroutines.
type fetcher struct {
	nc *client.Client
	mu sync.Mutex
}

// fetchInto loads the referenced record into the index, unless the reference
// is empty or the record was already loaded. Two tasks may ask for the same
// record at once, such as a currency that's both the transaction's and the
// subsidiary's; both fetch it, and the result is the same.
func (f *fetcher) fetchInto(ctx context.Context, recordType string, ref *Ref, index map[string]json.RawMessage) error {
	if ref == nil || ref.ID == "" {
		return nil
	}
	f.mu.Lock()
	_, ok := index[ref.ID]
	f.mu.Unlock()
	if ok {
		return nil
	}
	var data json.RawMessage
	if err := f.nc.GetRecord(ctx, recordType, ref.ID, false, &data); err != nil {
		return fmt.Errorf("fetching %s %s: %w", recordType, ref.ID, err)
	}
	f.mu.Lock()
	index[ref.ID] = data
	f.mu.Unlock()
	return nil
}

// records holds the typed versions of the records in a bundle.
type records struct {
	transaction *Transaction
	customer    *Customer
	subsidiary  *Subsidiary
	currencies  map[string]*Currency
	taxCodes    map[string]*SalesTaxItem
	related     map[string]*Transaction
	// taxCodeFields are the tax code records as plain JSON values, for
	// matching them with mappings.
	taxCodeFields map[string]map[string]any
}

func (b *Bundle) parse() (*records, error) {
	r := &records{
		transaction: new(Transaction),
		currencies:  make(map[string]*Currency, len(b.Currencies)),
		taxCodes:    make(map[string]*SalesTaxItem, len(b.TaxCodes)),
		related:     make(map[string]*Transaction, len(b.Related)),

		taxCodeFields: make(map[string]map[string]any, len(b.TaxCodes)),
	}
	if len(b.Transaction) == 0 {
		return nil, fmt.Errorf("bundle has no transaction")
	}
	if err := json.Unmarshal(b.Transaction, r.transaction); err != nil {
		return nil, fmt.Errorf("parsing transaction: %w", err)
	}
	if len(b.Customer) > 0 {
		r.customer = new(Customer)
		if err := json.Unmarshal(b.Customer, r.customer); err != nil {
			return nil, fmt.Errorf("parsing customer: %w", err)
		}
	}
	if len(b.Subsidiary) > 0 {
		r.subsidiary = new(Subsidiary)
		if err := json.Unmarshal(b.Subsidiary, r.subsidiary); err != nil {
			return nil, fmt.Errorf("parsing subsidiary: %w", err)
		}
	}
	for id, data := range b.Currencies {
		c := new(Currency)
		if err := json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("parsing currency %s: %w", id, err)
		}
		r.currencies[id] = c
	}
	for id, data := range b.TaxCodes {
		tc := new(SalesTaxItem)
		if err := json.Unmarshal(data, tc); err != nil {
			return nil, fmt.Errorf("parsing tax code %s: %w", id, err)
		}
		r.taxCodes[id] = tc
		fields := make(map[string]any)
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, fmt.Errorf("parsing tax code %s: %w", id, err)
		}
		r.taxCodeFields[id] = fields
	}
	for id, data := range b.Related {
		tx := new(Transaction)
		if err := json.Unmarshal(data, tx); err != nil {
			return nil, fmt.Errorf("parsing related transaction %s: %w", id, err)
		}
		r.related[id] = tx
	}
	return r, nil
}

// supplierCountry provides the country where the supplier accounts for tax:
// the subsidiary's or, in accounts without OneWorld, the supplier party's.
func (r *records) supplierCountry(supplier *org.Party) (l10n.TaxCountryCode, error) {
	if r.subsidiary != nil {
		if r.subsidiary.Country == nil || r.subsidiary.Country.ID == "" {
			return "", fmt.Errorf("subsidiary %s: country is required", r.subsidiary.ID)
		}
		return l10n.TaxCountryCode(r.subsidiary.Country.ID), nil
	}
	if supplier == nil {
		return "", fmt.Errorf("a subsidiary, or a supplier party in accounts without OneWorld, is required to determine the supplier")
	}
	if supplier.TaxID != nil && supplier.TaxID.Country != "" {
		return supplier.TaxID.Country, nil
	}
	for _, a := range supplier.Addresses {
		if a != nil && a.Country != "" {
			return l10n.TaxCountryCode(a.Country), nil
		}
	}
	return "", fmt.Errorf("supplier: a tax ID or address with a country is required")
}

// transactionType returns the transaction's type ID in lowercase, e.g.
// "custinvc".
func (r *records) transactionType() string {
	if r.transaction.Type == nil {
		return ""
	}
	return strings.ToLower(r.transaction.Type.ID)
}
