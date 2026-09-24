package goblnetsuite

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/invopop/gobl.netsuite/client"
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

	if tx.Entity != nil {
		if err := nc.GetRecord(ctx, "customer", tx.Entity.ID, true, &b.Customer); err != nil {
			return nil, fmt.Errorf("fetching customer %s: %w", tx.Entity.ID, err)
		}
	}

	var sub *Subsidiary
	if tx.Subsidiary != nil {
		if err := nc.GetRecord(ctx, "subsidiary", tx.Subsidiary.ID, true, &b.Subsidiary); err != nil {
			return nil, fmt.Errorf("fetching subsidiary %s: %w", tx.Subsidiary.ID, err)
		}
		sub = new(Subsidiary)
		if err := json.Unmarshal(b.Subsidiary, sub); err != nil {
			return nil, fmt.Errorf("parsing subsidiary %s: %w", tx.Subsidiary.ID, err)
		}
	}

	// Both the transaction currency and the subsidiary's base currency are
	// needed to describe exchange rates.
	currencies := []*Ref{tx.Currency}
	if sub != nil {
		currencies = append(currencies, sub.Currency)
	}
	for _, ref := range currencies {
		if err := fetchInto(ctx, nc, "currency", ref, b.Currencies); err != nil {
			return nil, err
		}
	}

	if tx.Item != nil {
		for _, it := range tx.Item.Items {
			// TODO: accounts may also use tax groups (record type "taxgroup")
			// that combine several tax codes, which are not supported yet.
			if err := fetchInto(ctx, nc, "salestaxitem", it.TaxCode, b.TaxCodes); err != nil {
				return nil, err
			}
		}
	}

	if err := fetchPrecedingInvoices(ctx, nc, tx, b); err != nil {
		return nil, err
	}

	return b, nil
}

// fetchPrecedingInvoices loads the invoices a transaction was created from
// or applied to. As the created from reference does not say what type of
// transaction it refers to, the types are looked up first so that only
// invoices are loaded, and not for example a return authorization.
func fetchPrecedingInvoices(ctx context.Context, nc *client.Client, tx *Transaction, b *Bundle) error {
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
	res, err := nc.Query(ctx, fmt.Sprintf(
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
		if err := fetchInto(ctx, nc, RecordTypeInvoice, &Ref{ID: row.ID}, b.Related); err != nil {
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

// fetchInto loads the referenced record into the index, unless the reference
// is empty or the record was already loaded.
func fetchInto(ctx context.Context, nc *client.Client, recordType string, ref *Ref, index map[string]json.RawMessage) error {
	if ref == nil || ref.ID == "" {
		return nil
	}
	if _, ok := index[ref.ID]; ok {
		return nil
	}
	var data json.RawMessage
	if err := nc.GetRecord(ctx, recordType, ref.ID, false, &data); err != nil {
		return fmt.Errorf("fetching %s %s: %w", recordType, ref.ID, err)
	}
	index[ref.ID] = data
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
}

func (b *Bundle) parse() (*records, error) {
	r := &records{
		transaction: new(Transaction),
		currencies:  make(map[string]*Currency, len(b.Currencies)),
		taxCodes:    make(map[string]*SalesTaxItem, len(b.TaxCodes)),
		related:     make(map[string]*Transaction, len(b.Related)),
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

// transactionType returns the transaction's type ID in lowercase, e.g.
// "custinvc".
func (r *records) transactionType() string {
	if r.transaction.Type == nil {
		return ""
	}
	return strings.ToLower(r.transaction.Type.ID)
}
