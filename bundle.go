package goblnetsuite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/invopop/gobl.netsuite/client"
)

// Bundle holds the raw NetSuite records needed to convert a transaction into
// a GOBL document. Records are kept exactly as returned by the REST API so
// that nothing is lost, including custom fields, and the bundle can be
// stored or handed to mappings as the conversion source.
type Bundle struct {
	Invoice    json.RawMessage `json:"invoice"`
	Customer   json.RawMessage `json:"customer,omitempty"`
	Subsidiary json.RawMessage `json:"subsidiary,omitempty"`
	// Currencies are indexed by internal ID.
	Currencies map[string]json.RawMessage `json:"currencies,omitempty"`
	// TaxCodes are indexed by internal ID.
	TaxCodes map[string]json.RawMessage `json:"taxCodes,omitempty"`
}

// FetchInvoice loads an invoice and the related records needed for its
// conversion from NetSuite.
func FetchInvoice(ctx context.Context, nc *client.Client, id string) (*Bundle, error) {
	b := &Bundle{
		Currencies: make(map[string]json.RawMessage),
		TaxCodes:   make(map[string]json.RawMessage),
	}
	if err := nc.GetRecord(ctx, "invoice", id, true, &b.Invoice); err != nil {
		return nil, fmt.Errorf("fetching invoice %s: %w", id, err)
	}
	inv := new(Invoice)
	if err := json.Unmarshal(b.Invoice, inv); err != nil {
		return nil, fmt.Errorf("parsing invoice %s: %w", id, err)
	}

	if inv.Entity != nil {
		if err := nc.GetRecord(ctx, "customer", inv.Entity.ID, true, &b.Customer); err != nil {
			return nil, fmt.Errorf("fetching customer %s: %w", inv.Entity.ID, err)
		}
	}

	var sub *Subsidiary
	if inv.Subsidiary != nil {
		if err := nc.GetRecord(ctx, "subsidiary", inv.Subsidiary.ID, true, &b.Subsidiary); err != nil {
			return nil, fmt.Errorf("fetching subsidiary %s: %w", inv.Subsidiary.ID, err)
		}
		sub = new(Subsidiary)
		if err := json.Unmarshal(b.Subsidiary, sub); err != nil {
			return nil, fmt.Errorf("parsing subsidiary %s: %w", inv.Subsidiary.ID, err)
		}
	}

	// Both the transaction currency and the subsidiary's base currency are
	// needed to describe exchange rates.
	currencies := []*Ref{inv.Currency}
	if sub != nil {
		currencies = append(currencies, sub.Currency)
	}
	for _, ref := range currencies {
		if err := fetchInto(ctx, nc, "currency", ref, b.Currencies); err != nil {
			return nil, err
		}
	}

	if inv.Item != nil {
		for _, it := range inv.Item.Items {
			// TODO: accounts may also use tax groups (record type "taxgroup")
			// that combine several tax codes, which are not supported yet.
			if err := fetchInto(ctx, nc, "salestaxitem", it.TaxCode, b.TaxCodes); err != nil {
				return nil, err
			}
		}
	}

	return b, nil
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
	invoice    *Invoice
	customer   *Customer
	subsidiary *Subsidiary
	currencies map[string]*Currency
	taxCodes   map[string]*SalesTaxItem
}

func (b *Bundle) parse() (*records, error) {
	r := &records{
		invoice:    new(Invoice),
		currencies: make(map[string]*Currency, len(b.Currencies)),
		taxCodes:   make(map[string]*SalesTaxItem, len(b.TaxCodes)),
	}
	if len(b.Invoice) == 0 {
		return nil, fmt.Errorf("bundle has no invoice")
	}
	if err := json.Unmarshal(b.Invoice, r.invoice); err != nil {
		return nil, fmt.Errorf("parsing invoice: %w", err)
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
	return r, nil
}
