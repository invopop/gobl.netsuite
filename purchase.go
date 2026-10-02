package netsuite

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/invopop/gobl/bill"
	"github.com/invopop/gobl/cbc"
	"github.com/invopop/gobl/currency"
	"github.com/invopop/gobl/l10n"
	"github.com/invopop/gobl/num"
	"github.com/invopop/gobl/org"
	"github.com/invopop/gobl/tax"
)

// Record types created from GOBL documents received from suppliers.
const (
	RecordTypeVendorBill   = "vendorBill"
	RecordTypeVendorCredit = "vendorCredit"
)

// maxMemo is the length of NetSuite memo fields.
const maxMemo = 999

// PurchaseOptions provides the NetSuite records a purchase refers to, which
// the caller finds in the account.
type PurchaseOptions struct {
	// Subsidiary is the internal ID of the subsidiary buying, the GOBL
	// customer.
	Subsidiary string
	// Country is where the subsidiary accounts for tax, used to choose tax
	// codes.
	Country l10n.TaxCountryCode
	// Vendor is the internal ID of the vendor, the GOBL supplier.
	Vendor string
	// Currency is the internal ID of the document's currency.
	Currency string
	// BaseCurrency is the subsidiary's currency, to set the exchange rate
	// from the document's rates when it differs.
	BaseCurrency currency.Code
	// Account is the internal ID of the expense account for every line.
	Account string
	// ExternalID identifies the record, such as the silo entry's ID, so it's
	// created only once.
	ExternalID string
	// PendingApproval creates bills pending approval, for accounts that
	// approve vendor bills.
	PendingApproval bool
	// TaxCodes finds the account's tax code for each tax combo.
	TaxCodes *TaxCodeIndex
}

// Outbound is a NetSuite record body prepared from a GOBL document.
type Outbound struct {
	// RecordType is the REST record type to create, such as vendorBill.
	RecordType string `json:"record_type"`
	// Body is the record to send.
	Body map[string]any `json:"body"`
	// Notices describe parts of the document that were not sent.
	Notices []*Notice `json:"notices,omitempty"`
}

func (o *Outbound) notice(path, format string, args ...any) {
	o.Notices = append(o.Notices, &Notice{Path: path, Message: fmt.Sprintf(format, args...)})
}

// expenseLine is a line of a vendor bill or credit's expense sublist, with
// the tax combo used to find its tax code.
type expenseLine struct {
	amount num.Amount
	tax    num.Amount
	combo  *tax.Combo
	code   *SalesTaxItem
	memo   string
}

// NewPurchase prepares a vendor bill, or a vendor credit for a credit note,
// from a calculated GOBL invoice received from a supplier. Each line, charge
// and discount becomes an expense line in the account given, with its tax
// amount set so the tax totals match the invoice's, as NetSuite would
// otherwise round tax per line.
func NewPurchase(inv *bill.Invoice, o *PurchaseOptions) (*Outbound, error) {
	if inv == nil || inv.Totals == nil {
		return nil, fmt.Errorf("the invoice must be calculated")
	}
	if o == nil || o.Subsidiary == "" || o.Vendor == "" || o.Account == "" || o.TaxCodes == nil {
		return nil, fmt.Errorf("the subsidiary, vendor, expense account and tax codes are required")
	}
	if inv.Totals.RetainedTax != nil && !inv.Totals.RetainedTax.IsZero() {
		return nil, fmt.Errorf("invoices with retained taxes, such as withholding, are not supported yet")
	}

	out := &Outbound{RecordType: RecordTypeVendorBill}
	switch inv.Type {
	case bill.InvoiceTypeStandard:
	case bill.InvoiceTypeCreditNote:
		out.RecordType = RecordTypeVendorCredit
	default:
		return nil, fmt.Errorf("unsupported invoice type %q", inv.Type)
	}

	exp := inv.Currency.Def().Subunits
	lines, err := expenseLines(inv)
	if err != nil {
		return nil, err
	}
	if err := setLineTaxes(inv, lines, o, exp); err != nil {
		return nil, err
	}
	out.Body = purchaseHeader(inv, o, out.RecordType)
	out.Body["expense"] = map[string]any{"items": expenseItems(lines, o, exp)}
	if out.RecordType == RecordTypeVendorCredit && len(inv.Preceding) > 0 {
		out.notice("preceding", "the vendor credit is not applied to the bill it corrects")
	}
	if inv.Payment != nil && len(inv.Payment.Advances) > 0 {
		out.notice("payment.advances", "advances are not recorded as payments")
	}
	return out, nil
}

// expenseLines prepares an expense line for each of the invoice's lines,
// charges and discounts.
func expenseLines(inv *bill.Invoice) ([]*expenseLine, error) {
	var lines []*expenseLine
	for i, l := range inv.Lines {
		combo, err := lineCombo(l.Taxes)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if l.Total == nil {
			return nil, fmt.Errorf("line %d: missing total", i+1)
		}
		lines = append(lines, &expenseLine{amount: *l.Total, combo: combo, memo: lineMemo(l)})
	}
	for i, c := range inv.Charges {
		combo, err := lineCombo(c.Taxes)
		if err != nil {
			return nil, fmt.Errorf("charge %d: %w", i+1, err)
		}
		lines = append(lines, &expenseLine{amount: c.Amount, combo: combo, memo: firstOf(c.Reason, "Charge")})
	}
	for i, d := range inv.Discounts {
		combo, err := lineCombo(d.Taxes)
		if err != nil {
			return nil, fmt.Errorf("discount %d: %w", i+1, err)
		}
		lines = append(lines, &expenseLine{amount: d.Amount.Invert(), combo: combo, memo: firstOf(d.Reason, "Discount")})
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("the invoice has no lines")
	}
	return lines, nil
}

// setLineTaxes finds each line's tax code, and its tax amount, balanced to
// the invoice's tax totals.
func setLineTaxes(inv *bill.Invoice, lines []*expenseLine, o *PurchaseOptions, exp uint32) error {
	for i, l := range lines {
		if l.combo == nil {
			continue
		}
		code, err := o.TaxCodes.Find(l.combo, o.Country, DirectionPurchase)
		if err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
		l.code = code
		if l.combo.Percent != nil {
			l.tax = l.combo.Percent.Of(l.amount).Rescale(exp)
		} else {
			l.tax = num.MakeAmount(0, exp)
		}
	}
	balanceTaxes(inv, lines, exp)
	return nil
}

func expenseItems(lines []*expenseLine, o *PurchaseOptions, exp uint32) []map[string]any {
	items := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		item := map[string]any{
			"account": ref(o.Account),
			"amount":  json.Number(l.amount.Rescale(exp).String()),
			"memo":    truncate(l.memo, maxMemo),
		}
		if l.code != nil {
			item["taxCode"] = ref(l.code.ID)
			item["tax1Amt"] = json.Number(l.tax.String())
		}
		items = append(items, item)
	}
	return items
}

// purchaseHeader prepares the record's body fields, other than its lines.
func purchaseHeader(inv *bill.Invoice, o *PurchaseOptions, recordType string) map[string]any {
	body := map[string]any{
		"entity":     ref(o.Vendor),
		"subsidiary": ref(o.Subsidiary),
		"tranId":     documentNumber(inv.Series, inv.Code),
		"tranDate":   inv.IssueDate.String(),
	}
	if o.ExternalID != "" {
		body["externalId"] = o.ExternalID
	}
	if o.Currency != "" {
		body["currency"] = ref(o.Currency)
	}
	if rate := exchangeRate(inv, o.BaseCurrency); rate != "" {
		body["exchangeRate"] = json.Number(rate)
	}
	if memo := purchaseMemo(inv); memo != "" {
		body["memo"] = truncate(memo, maxMemo)
	}
	if recordType == RecordTypeVendorBill {
		if due := dueDate(inv); due != "" {
			body["dueDate"] = due
		}
		if o.PendingApproval {
			body["approvalStatus"] = ref("1") // Pending Approval
		}
	}
	return body
}

// lineCombo provides the only tax combo of a line, or nil for lines without
// tax.
func lineCombo(set tax.Set) (*tax.Combo, error) {
	var found *tax.Combo
	for _, c := range set {
		if found != nil {
			return nil, fmt.Errorf("lines with more than one tax are not supported yet")
		}
		found = c
	}
	return found, nil
}

// balanceTaxes adjusts the line tax amounts of each rate so they add up to
// the invoice's tax total for the rate, putting the difference on the
// largest line.
func balanceTaxes(inv *bill.Invoice, lines []*expenseLine, exp uint32) {
	if inv.Totals.Taxes == nil {
		return
	}
	for _, cat := range inv.Totals.Taxes.Categories {
		for _, rate := range cat.Rates {
			var group []*expenseLine
			sum := num.MakeAmount(0, exp)
			for _, l := range lines {
				if l.combo != nil && l.combo.Category == cat.Code && sameRate(l.combo, rate) {
					group = append(group, l)
					sum = sum.Add(l.tax)
				}
			}
			if len(group) == 0 {
				continue
			}
			diff := rate.Amount.Rescale(exp).Subtract(sum)
			if diff.IsZero() {
				continue
			}
			largest := group[0]
			for _, l := range group[1:] {
				if l.amount.Abs().Compare(largest.amount.Abs()) > 0 {
					largest = l
				}
			}
			largest.tax = largest.tax.Add(diff)
		}
	}
}

// sameRate checks whether a line's combo is part of a rate total.
func sameRate(c *tax.Combo, r *tax.RateTotal) bool {
	if c.Country != r.Country || normalKey(c.Key) != normalKey(r.Key) || !c.Ext.Equals(r.Ext) {
		return false
	}
	if c.Percent == nil || r.Percent == nil {
		return c.Percent == nil && r.Percent == nil
	}
	return c.Percent.Equals(*r.Percent)
}

// documentNumber joins a series and code as GOBL presents them in formats
// without a separate series.
func documentNumber(series, code cbc.Code) string {
	if series == "" {
		return code.String()
	}
	return series.String() + "-" + code.String()
}

// exchangeRate provides the invoice's rate to the base currency, if it
// differs and the invoice has one.
func exchangeRate(inv *bill.Invoice, base currency.Code) string {
	if base == "" || base == inv.Currency {
		return ""
	}
	for _, r := range inv.ExchangeRates {
		if r.From == inv.Currency && r.To == base {
			return r.Amount.String()
		}
	}
	return ""
}

// dueDate provides the first due date of the payment terms.
func dueDate(inv *bill.Invoice) string {
	if inv.Payment == nil || inv.Payment.Terms == nil {
		return ""
	}
	for _, dd := range inv.Payment.Terms.DueDates {
		if dd != nil && dd.Date != nil {
			return dd.Date.String()
		}
	}
	return ""
}

// purchaseMemo describes the purchase with the invoice's general note, or
// the buyer reference it was ordered with.
func purchaseMemo(inv *bill.Invoice) string {
	for _, n := range inv.Notes {
		if n != nil && n.Key == org.NoteKeyGeneral && n.Text != "" {
			return n.Text
		}
	}
	if inv.Ordering != nil && inv.Ordering.Code != "" {
		return "Order " + inv.Ordering.Code.String()
	}
	return ""
}

// lineMemo describes a line by its quantity and item name.
func lineMemo(l *bill.Line) string {
	if l.Item == nil {
		return ""
	}
	name := l.Item.Name
	if l.Quantity.Compare(num.MakeAmount(1, 0)) != 0 {
		name = l.Quantity.String() + " × " + name
	}
	return name
}

func ref(id string) map[string]string {
	return map[string]string{"id": id}
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// CheckRecordTotals compares the totals of a record NetSuite created from an
// invoice with the invoice's, as NetSuite calculates them itself. As when
// importing, each tax rate may differ by one subunit.
func CheckRecordTotals(inv *bill.Invoice, record json.RawMessage) error {
	rec := struct {
		Total     json.Number `json:"total"`
		UserTotal json.Number `json:"userTotal"`
		TaxTotal  json.Number `json:"taxTotal"`
	}{}
	if err := json.Unmarshal(record, &rec); err != nil {
		return fmt.Errorf("parsing record: %w", err)
	}
	exp := inv.Currency.Def().Subunits
	rates := 0
	if inv.Totals.Taxes != nil {
		for _, c := range inv.Totals.Taxes.Categories {
			rates += len(c.Rates)
		}
	}
	tolerance := num.MakeAmount(int64(rates), exp)

	var problems []string
	check := func(name string, got json.Number, want num.Amount) {
		if got == "" {
			return
		}
		a, err := num.AmountFromString(got.String())
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s %q: %s", name, got, err))
			return
		}
		diff := a.Rescale(exp).Subtract(want.Rescale(exp)).Abs()
		if diff.Compare(tolerance) > 0 {
			problems = append(problems, fmt.Sprintf("%s is %s in NetSuite, but %s in the invoice", name, a.Rescale(exp), want.Rescale(exp)))
		}
	}
	check("total", json.Number(firstOf(rec.Total.String(), rec.UserTotal.String())), inv.Totals.TotalWithTax)
	check("taxTotal", rec.TaxTotal, inv.Totals.Tax)
	if len(problems) > 0 {
		return fmt.Errorf("totals differ: %s", strings.Join(problems, "; "))
	}
	return nil
}
