package goblnetsuite

import "encoding/json"

// The types in this file model the subset of NetSuite REST record fields
// used by the conversion. They are deliberately partial: fields vary between
// accounts depending on the features enabled, so everything else is left in
// the raw records of the Bundle for mappings to use.
//
// Numbers are decoded as json.Number so amounts keep their exact textual
// representation instead of passing through a float64.

// Ref is the reference NetSuite uses for related records and enumerations.
type Ref struct {
	ID      string `json:"id"`
	RefName string `json:"refName,omitempty"`
}

// Address is a NetSuite address subrecord.
type Address struct {
	Addressee string `json:"addressee,omitempty"`
	Attention string `json:"attention,omitempty"`
	Addr1     string `json:"addr1,omitempty"`
	Addr2     string `json:"addr2,omitempty"`
	Addr3     string `json:"addr3,omitempty"`
	City      string `json:"city,omitempty"`
	State     string `json:"state,omitempty"`
	Zip       string `json:"zip,omitempty"`
	Country   *Ref   `json:"country,omitempty"`
}

// Invoice is a NetSuite invoice record (type "invoice").
type Invoice struct {
	ID             string        `json:"id"`
	TranID         string        `json:"tranId"`
	TranDate       string        `json:"tranDate"`
	DueDate        string        `json:"dueDate,omitempty"`
	Entity         *Ref          `json:"entity,omitempty"`
	Subsidiary     *Ref          `json:"subsidiary,omitempty"`
	Currency       *Ref          `json:"currency,omitempty"`
	ExchangeRate   json.Number   `json:"exchangeRate,omitempty"`
	Terms          *Ref          `json:"terms,omitempty"`
	OtherRefNum    string        `json:"otherRefNum,omitempty"`
	Memo           string        `json:"memo,omitempty"`
	VATRegNum      string        `json:"vatRegNum,omitempty"`
	Email          string        `json:"email,omitempty"`
	BillingAddress *Address      `json:"billingAddress,omitempty"`
	Item           *InvoiceItems `json:"item,omitempty"`

	// Subtotal is the sum of the lines, including discount lines, but
	// before the header discount in DiscountTotal (a negative amount).
	Subtotal      json.Number `json:"subtotal,omitempty"`
	DiscountItem  *Ref        `json:"discountItem,omitempty"`
	DiscountTotal json.Number `json:"discountTotal,omitempty"`
	ShippingCost  json.Number `json:"shippingCost,omitempty"`
	TaxTotal      json.Number `json:"taxTotal,omitempty"`
	Total         json.Number `json:"total,omitempty"`
}

// InvoiceItems is the expanded item sublist of an invoice.
type InvoiceItems struct {
	Items []*InvoiceItem `json:"items"`
}

// InvoiceItem is a single line of an invoice's item sublist.
type InvoiceItem struct {
	Line        int         `json:"line"`
	Item        *Ref        `json:"item,omitempty"`
	ItemType    *Ref        `json:"itemType,omitempty"`
	Description string      `json:"description,omitempty"`
	Quantity    json.Number `json:"quantity,omitempty"`
	Rate        json.Number `json:"rate,omitempty"`
	Amount      json.Number `json:"amount,omitempty"`
	TaxCode     *Ref        `json:"taxCode,omitempty"`
	TaxRate1    json.Number `json:"taxRate1,omitempty"`
	Tax1Amt     json.Number `json:"tax1Amt,omitempty"`
}

// Customer is a NetSuite customer record.
type Customer struct {
	ID           string `json:"id"`
	EntityID     string `json:"entityId,omitempty"`
	IsPerson     bool   `json:"isPerson"`
	CompanyName  string `json:"companyName,omitempty"`
	FirstName    string `json:"firstName,omitempty"`
	LastName     string `json:"lastName,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	VATRegNumber string `json:"vatRegNumber,omitempty"`
}

// Subsidiary is a NetSuite subsidiary record, only present in OneWorld
// accounts, which provides the supplier details.
type Subsidiary struct {
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	LegalName       string   `json:"legalName,omitempty"`
	FederalIDNumber string   `json:"federalIdNumber,omitempty"`
	Email           string   `json:"email,omitempty"`
	Country         *Ref     `json:"country,omitempty"`
	Currency        *Ref     `json:"currency,omitempty"`
	MainAddress     *Address `json:"mainAddress,omitempty"`
}

// Currency is a NetSuite currency record. The Symbol field holds the ISO
// 4217 code, while Name is a label defined by the account.
type Currency struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Symbol string `json:"symbol"`
}

// SalesTaxItem is a NetSuite tax code (record type "salestaxitem") as used
// by accounts with legacy tax, rather than SuiteTax.
type SalesTaxItem struct {
	ID            string      `json:"id"`
	ItemID        string      `json:"itemId,omitempty"`
	Description   string      `json:"description,omitempty"`
	Rate          json.Number `json:"rate,omitempty"`
	TaxType       *Ref        `json:"taxType,omitempty"`
	NexusCountry  *Ref        `json:"nexusCountry,omitempty"`
	Exempt        bool        `json:"exempt"`
	Export        bool        `json:"export"`
	ReverseCharge bool        `json:"reverseCharge"`
	ECCode        bool        `json:"ecCode"`
	Service       bool        `json:"service"`
}
