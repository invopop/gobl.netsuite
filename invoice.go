package goblnetsuite

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/invopop/gobl/bill"
	"github.com/invopop/gobl/cal"
	"github.com/invopop/gobl/cbc"
	"github.com/invopop/gobl/currency"
	"github.com/invopop/gobl/l10n"
	"github.com/invopop/gobl/num"
	"github.com/invopop/gobl/org"
	"github.com/invopop/gobl/pay"
	"github.com/invopop/gobl/tax"
)

// Meta keys added to GOBL documents converted from NetSuite.
const (
	MetaKeyNetSuiteID   cbc.Key = "netsuite-id"
	MetaKeyNetSuiteType cbc.Key = "netsuite-type"
)

// Result is the outcome of converting a NetSuite transaction.
type Result struct {
	// Invoice is the GOBL invoice, not yet calculated, so that mappings can
	// be applied before calculating and validating it.
	Invoice *bill.Invoice `json:"invoice"`

	// Source is the bundle the invoice was converted from.
	Source *Bundle `json:"source"`

	// SourceLines holds the raw NetSuite item line for each GOBL line, in the
	// same order as the invoice lines. NetSuite lines that do not produce a
	// GOBL line, such as subtotals, are not included.
	SourceLines []json.RawMessage `json:"source_lines"`

	// Unmapped lists source data the conversion did not use and may need a
	// mapping, such as custom fields or unsupported line types.
	Unmapped []*Notice `json:"unmapped,omitempty"`

	// Warnings lists accepted differences with NetSuite, such as tax
	// rounding, found by CheckTotals.
	Warnings []*Notice `json:"warnings,omitempty"`

	records *records

	// headerDiscountRates is the number of tax codes a header discount was
	// shared between.
	headerDiscountRates int
}

// Notice describes a piece of source data that was not converted.
type Notice struct {
	// Path locates the data in the source bundle, e.g. "transaction.custbody_ref".
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (r *Result) notice(path, format string, args ...any) {
	r.Unmapped = append(r.Unmapped, &Notice{Path: path, Message: fmt.Sprintf(format, args...)})
}

// FromInvoice converts a bundle containing a NetSuite invoice into a GOBL
// invoice.
//
// The conversion supports accounts using legacy tax, with a tax code per
// line, and requires OneWorld, as the supplier is taken from the subsidiary.
// The resulting invoice is not calculated: apply any mappings, calculate, and
// then call CheckTotals to compare with the amounts calculated by NetSuite.
func FromInvoice(b *Bundle) (*Result, error) {
	return convert(b, transactionTypeInvoice)
}

// FromCreditMemo converts a bundle containing a NetSuite credit memo into a
// GOBL credit note, referring to the invoices it was created from or applied
// to when they are included in the bundle. See FromInvoice for details.
func FromCreditMemo(b *Bundle) (*Result, error) {
	return convert(b, transactionTypeCreditMemo)
}

// Convert converts a bundle into a GOBL invoice or credit note according to
// the type of its transaction.
func Convert(b *Bundle) (*Result, error) {
	recs, err := b.parse()
	if err != nil {
		return nil, err
	}
	switch t := recs.transactionType(); t {
	case transactionTypeInvoice:
		return FromInvoice(b)
	case transactionTypeCreditMemo:
		return FromCreditMemo(b)
	default:
		return nil, fmt.Errorf("unsupported transaction type %q", t)
	}
}

func convert(b *Bundle, txType string) (*Result, error) {
	recs, err := b.parse()
	if err != nil {
		return nil, err
	}
	src := recs.transaction
	if t := recs.transactionType(); t != txType {
		return nil, fmt.Errorf("transaction %s: expected type %q, got %q", src.ID, txType, t)
	}
	res := &Result{Source: b, records: recs}

	if recs.subsidiary == nil {
		return nil, fmt.Errorf("transaction %s: subsidiary is required to determine the supplier", src.ID)
	}
	if recs.subsidiary.Country == nil || recs.subsidiary.Country.ID == "" {
		return nil, fmt.Errorf("subsidiary %s: country is required", recs.subsidiary.ID)
	}
	country := l10n.TaxCountryCode(recs.subsidiary.Country.ID)

	inv := &bill.Invoice{
		Regime: tax.WithRegime(country),
		Type:   bill.InvoiceTypeStandard,
		Code:   cbc.Code(src.TranID),
		// NetSuite rounds line amounts, and the tax of each rate, to the
		// currency's precision before adding them up.
		Tax: &bill.Tax{Rounding: tax.RoundingRuleCurrency},
		Meta: cbc.Meta{
			MetaKeyNetSuiteID:   src.ID,
			MetaKeyNetSuiteType: RecordTypeInvoice,
		},
	}
	res.Invoice = inv
	if txType == transactionTypeCreditMemo {
		inv.Type = bill.InvoiceTypeCreditNote
		inv.Meta[MetaKeyNetSuiteType] = RecordTypeCreditMemo
	}

	if inv.IssueDate, err = parseDate(src.TranDate); err != nil {
		return nil, fmt.Errorf("transaction tranDate: %w", err)
	}

	if err := res.setCurrency(); err != nil {
		return nil, err
	}

	inv.Supplier = newSupplier(recs.subsidiary)
	inv.Customer = newCustomer(src, recs.customer)

	if err := res.setLines(country); err != nil {
		return nil, err
	}

	if err := res.setPreceding(); err != nil {
		return nil, err
	}

	if err := res.setPayment(); err != nil {
		return nil, err
	}

	if src.OtherRefNum != "" {
		inv.Ordering = &bill.Ordering{Code: cbc.Code(src.OtherRefNum)}
	}

	res.checkHeaderAmounts()
	res.findCustomFields()

	return res, nil
}

// setPreceding refers to the invoices a credit memo was created from or
// applied to. Invoices missing from the bundle's related transactions are
// reported, as some regimes require the preceding document.
func (r *Result) setPreceding() error {
	src := r.records.transaction
	for _, id := range precedingIDs(src) {
		pre, ok := r.records.related[id]
		if !ok {
			continue
		}
		d, err := parseDate(pre.TranDate)
		if err != nil {
			return fmt.Errorf("related transaction %s tranDate: %w", id, err)
		}
		r.Invoice.Preceding = append(r.Invoice.Preceding, &org.DocumentRef{
			Type:      bill.InvoiceTypeStandard,
			Code:      cbc.Code(pre.TranID),
			IssueDate: &d,
			Meta:      cbc.Meta{MetaKeyNetSuiteID: pre.ID},
		})
	}
	if r.Invoice.Type == bill.InvoiceTypeCreditNote && len(r.Invoice.Preceding) == 0 {
		r.notice("transaction", "credit memo not created from or applied to an invoice")
	}
	return nil
}

// setCurrency sets the invoice currency using the ISO code from the currency
// record, adding an exchange rate when it differs from the subsidiary's base
// currency.
func (r *Result) setCurrency() error {
	src := r.records.transaction
	cur, err := r.currency(src.Currency)
	if err != nil {
		return fmt.Errorf("invoice currency: %w", err)
	}
	r.Invoice.Currency = cur

	base, err := r.currency(r.records.subsidiary.Currency)
	if err != nil {
		return fmt.Errorf("subsidiary currency: %w", err)
	}
	if base == cur {
		return nil
	}
	// NetSuite's exchange rate converts the transaction currency into the
	// subsidiary's base currency.
	rate, err := parseAmount(src.ExchangeRate)
	if err != nil {
		return fmt.Errorf("invoice exchangeRate: %w", err)
	}
	r.Invoice.ExchangeRates = []*currency.ExchangeRate{
		{From: cur, To: base, Amount: rate},
	}
	return nil
}

func (r *Result) currency(ref *Ref) (currency.Code, error) {
	if ref == nil || ref.ID == "" {
		return currency.CodeEmpty, fmt.Errorf("missing")
	}
	c, ok := r.records.currencies[ref.ID]
	if !ok {
		return currency.CodeEmpty, fmt.Errorf("currency %s not in bundle", ref.ID)
	}
	code := currency.Code(strings.ToUpper(c.Symbol))
	if code.Def() == nil {
		return currency.CodeEmpty, fmt.Errorf("currency %s: unknown ISO code %q", ref.ID, c.Symbol)
	}
	return code, nil
}

// setPayment adds the payment terms and due date.
func (r *Result) setPayment() error {
	src := r.records.transaction
	if src.DueDate == "" && src.Terms == nil {
		return nil
	}
	terms := new(pay.Terms)
	if src.Terms != nil {
		terms.Notes = src.Terms.RefName
	}
	if src.DueDate != "" {
		d, err := parseDate(src.DueDate)
		if err != nil {
			return fmt.Errorf("invoice dueDate: %w", err)
		}
		terms.DueDates = []*pay.DueDate{
			{Date: &d, Percent: num.NewPercentage(100, 2)},
		}
	}
	r.Invoice.Payment = &bill.PaymentDetails{Terms: terms}
	return nil
}

// checkHeaderAmounts flags header level amounts not yet supported, which
// would otherwise only be noticed as a difference in totals.
func (r *Result) checkHeaderAmounts() {
	if a, err := parseAmount(r.records.transaction.ShippingCost); err == nil && !a.IsZero() {
		r.notice("transaction.shippingCost", "not supported yet")
	}
}

// findCustomFields lists custom body and line fields with values, as these
// are specific to each account and can only be converted with a mapping.
func (r *Result) findCustomFields() {
	body := make(map[string]json.RawMessage)
	if err := json.Unmarshal(r.Source.Transaction, &body); err != nil {
		return
	}
	for _, k := range slices.Sorted(maps.Keys(body)) {
		if strings.HasPrefix(k, "custbody") && hasValue(body[k]) {
			r.notice("transaction."+k, "custom field")
		}
	}
	for i, line := range r.SourceLines {
		fields := make(map[string]json.RawMessage)
		if err := json.Unmarshal(line, &fields); err != nil {
			continue
		}
		for _, k := range slices.Sorted(maps.Keys(fields)) {
			if strings.HasPrefix(k, "custcol") && hasValue(fields[k]) {
				r.notice(fmt.Sprintf("source_lines[%d].%s", i, k), "custom field")
			}
		}
	}
}

// CheckTotals compares the totals of the calculated invoice with those
// calculated by NetSuite, returning an error describing any differences.
// Differences usually mean the conversion, or a mapping, is missing data.
func (r *Result) CheckTotals() error {
	t := r.Invoice.Totals
	if t == nil {
		return fmt.Errorf("invoice has not been calculated")
	}
	src := r.records.transaction
	subtotal, err := r.money(src.Subtotal)
	if err != nil {
		return fmt.Errorf("invoice subtotal: %w", err)
	}
	discount, err := r.money(src.DiscountTotal)
	if err != nil {
		return fmt.Errorf("invoice discountTotal: %w", err)
	}
	// NetSuite rounds the tax of a header discount separately from the tax
	// of the lines for each rate, so the tax can differ from GOBL's, which is
	// calculated on the net base, by up to one subunit per rate.
	exp := r.Invoice.Currency.Def().Subunits
	tolerance := num.MakeAmount(int64(r.headerDiscountRates), exp)

	var diffs []string
	for _, c := range []struct {
		name      string
		want      json.Number
		actual    num.Amount
		tolerance num.Amount
	}{
		// Discount lines may become invoice discounts in GOBL, so the sum of
		// lines is not comparable, only the net total after all discounts.
		{"subtotal+discountTotal", json.Number(subtotal.Add(discount).String()), t.Total, num.AmountZero},
		{"taxTotal", src.TaxTotal, t.Tax, tolerance},
		{"total", src.Total, t.TotalWithTax, tolerance},
	} {
		want, err := r.money(c.want)
		if err != nil {
			return fmt.Errorf("invoice %s: %w", c.name, err)
		}
		if want.Equals(c.actual) {
			continue
		}
		diff := want.Subtract(c.actual).Abs()
		if diff.Compare(c.tolerance) <= 0 {
			r.Warnings = append(r.Warnings, &Notice{
				Path: "transaction." + c.name,
				Message: fmt.Sprintf("netsuite %s, gobl %s: header discount tax rounded separately by netsuite",
					want, c.actual),
			})
			continue
		}
		diffs = append(diffs, fmt.Sprintf("%s: netsuite %s, gobl %s", c.name, want, c.actual))
	}
	if len(diffs) > 0 {
		return fmt.Errorf("totals differ from netsuite: %s", strings.Join(diffs, "; "))
	}
	return nil
}

func parseDate(s string) (cal.Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return cal.Date{}, err
	}
	return cal.DateOf(t), nil
}

// money parses a NetSuite currency amount, with at least the precision of
// the invoice currency. NetSuite numbers have varying precision, e.g. "100.0"
// or "3166.67", and amount arithmetic keeps the precision of the left hand
// operand, so amounts must be scaled before they are added or subtracted.
func (r *Result) money(n json.Number) (num.Amount, error) {
	a, err := parseAmount(n)
	if err != nil {
		return a, err
	}
	return a.RescaleUp(r.Invoice.Currency.Def().Subunits), nil
}

// parseAmount converts a NetSuite number into an amount, where an empty value
// is zero.
func parseAmount(n json.Number) (num.Amount, error) {
	if n == "" {
		return num.AmountZero, nil
	}
	return num.AmountFromString(n.String())
}

func hasValue(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s != "" && s != "null" && s != `""` && s != "{}" && s != "[]"
}
