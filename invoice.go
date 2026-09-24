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

	records *records
}

// Notice describes a piece of source data that was not converted.
type Notice struct {
	// Path locates the data in the source bundle, e.g. "invoice.custbody_ref".
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
	recs, err := b.parse()
	if err != nil {
		return nil, err
	}
	src := recs.invoice
	res := &Result{Source: b, records: recs}

	if recs.subsidiary == nil {
		return nil, fmt.Errorf("invoice %s: subsidiary is required to determine the supplier", src.ID)
	}
	if recs.subsidiary.Country == nil || recs.subsidiary.Country.ID == "" {
		return nil, fmt.Errorf("subsidiary %s: country is required", recs.subsidiary.ID)
	}
	country := l10n.TaxCountryCode(recs.subsidiary.Country.ID)

	inv := &bill.Invoice{
		Regime: tax.WithRegime(country),
		Type:   bill.InvoiceTypeStandard,
		Code:   cbc.Code(src.TranID),
		Meta: cbc.Meta{
			MetaKeyNetSuiteID:   src.ID,
			MetaKeyNetSuiteType: "invoice",
		},
	}
	res.Invoice = inv

	if inv.IssueDate, err = parseDate(src.TranDate); err != nil {
		return nil, fmt.Errorf("invoice tranDate: %w", err)
	}

	if err := res.setCurrency(); err != nil {
		return nil, err
	}

	inv.Supplier = newSupplier(recs.subsidiary)
	inv.Customer = newCustomer(src, recs.customer)

	if err := res.setLines(country); err != nil {
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

// setCurrency sets the invoice currency using the ISO code from the currency
// record, adding an exchange rate when it differs from the subsidiary's base
// currency.
func (r *Result) setCurrency() error {
	src := r.records.invoice
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
	src := r.records.invoice
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
	src := r.records.invoice
	for path, n := range map[string]json.Number{
		"invoice.discountTotal": src.DiscountTotal,
		"invoice.shippingCost":  src.ShippingCost,
	} {
		if a, err := parseAmount(n); err == nil && !a.IsZero() {
			r.notice(path, "not supported yet")
		}
	}
}

// findCustomFields lists custom body and line fields with values, as these
// are specific to each account and can only be converted with a mapping.
func (r *Result) findCustomFields() {
	body := make(map[string]json.RawMessage)
	if err := json.Unmarshal(r.Source.Invoice, &body); err != nil {
		return
	}
	for _, k := range slices.Sorted(maps.Keys(body)) {
		if strings.HasPrefix(k, "custbody") && hasValue(body[k]) {
			r.notice("invoice."+k, "custom field")
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
	src := r.records.invoice
	var diffs []string
	for _, c := range []struct {
		name   string
		want   json.Number
		actual num.Amount
	}{
		{"subtotal", src.Subtotal, t.Sum},
		{"taxTotal", src.TaxTotal, t.Tax},
		{"total", src.Total, t.TotalWithTax},
	} {
		want, err := parseAmount(c.want)
		if err != nil {
			return fmt.Errorf("invoice %s: %w", c.name, err)
		}
		if !want.Equals(c.actual) {
			diffs = append(diffs, fmt.Sprintf("%s: netsuite %s, gobl %s", c.name, want, c.actual))
		}
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
