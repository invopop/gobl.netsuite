package netsuite

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/invopop/gobl/bill"
	"github.com/invopop/gobl/cbc"
	"github.com/invopop/gobl/l10n"
	"github.com/invopop/gobl/num"
	"github.com/invopop/gobl/org"
	"github.com/invopop/gobl/tax"
)

// Item types, as used in the itemType field of invoice lines. Service,
// NonInvtPart, Discount and Subtotal have been seen in responses; the others
// are the standard NetSuite item type IDs.
const (
	itemTypeAssembly    = "Assembly"
	itemTypeDescription = "Description"
	itemTypeDiscount    = "Discount"
	itemTypeDownload    = "DwnLdItem"
	itemTypeEndGroup    = "EndGroup"
	itemTypeGiftCert    = "GiftCert"
	itemTypeGroup       = "Group"
	itemTypeInventory   = "InvtPart"
	itemTypeKit         = "Kit"
	itemTypeNonInv      = "NonInvtPart"
	itemTypeOtherCharge = "OthCharge"
	itemTypeService     = "Service"
	itemTypeSubtotal    = "Subtotal"
)

// itemTypesSold are the item types converted into regular lines.
var itemTypesSold = []string{
	itemTypeAssembly,
	itemTypeDownload,
	itemTypeGiftCert,
	itemTypeInventory,
	itemTypeKit,
	itemTypeNonInv,
	itemTypeOtherCharge,
	itemTypeService,
}

// itemTypesIgnored are the item types that do not affect the invoice amounts
// and so are safe to skip. Group markers are skipped as the members of the
// group are included as regular lines, while the end of group line repeats
// their total.
var itemTypesIgnored = []string{
	itemTypeEndGroup,
	itemTypeGroup,
	itemTypeSubtotal,
}

// taxBase accumulates the net amount of lines sharing a tax code, used to
// share header discounts between tax codes as NetSuite does.
type taxBase struct {
	item   *TransactionItem // first line with the tax code, to build combos
	path   string
	amount num.Amount
}

// setLines converts the invoice's item sublist into GOBL lines and
// discounts.
func (r *Result) setLines(country l10n.TaxCountryCode) error {
	src := r.records.transaction
	if src.Item == nil {
		return nil
	}
	var raw struct {
		Item struct {
			Items []json.RawMessage `json:"items"`
		} `json:"item"`
	}
	if err := json.Unmarshal(r.Source.Transaction, &raw); err != nil {
		return fmt.Errorf("parsing invoice lines: %w", err)
	}

	var bases []*taxBase
	addBase := func(path string, it *TransactionItem, amount num.Amount) {
		if it.TaxCode == nil || it.TaxCode.ID == "" {
			return
		}
		for _, b := range bases {
			if b.item.TaxCode.ID == it.TaxCode.ID {
				b.amount = b.amount.Add(amount)
				return
			}
		}
		bases = append(bases, &taxBase{item: it, path: path, amount: amount})
	}

	// prev is the source and GOBL line directly preceding a discount line,
	// which the discount applies to.
	var prev *TransactionItem
	var prevLine *bill.Line

	for i, it := range src.Item.Items {
		path := fmt.Sprintf("transaction.item.items[%d]", i)
		itemType := ""
		if it.ItemType != nil {
			itemType = it.ItemType.ID
		}
		amount, err := r.money(it.Amount)
		if err != nil {
			return fmt.Errorf("%s: amount: %w", path, err)
		}

		switch {
		case slices.Contains(itemTypesSold, itemType):
			line, err := r.newLine(path, it, amount, country)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			r.Invoice.Lines = append(r.Invoice.Lines, line)
			r.track(ScopeLine, line, raw.Item.Items[i])
			addBase(path, it, amount)
			prev, prevLine = it, line
			continue
		case itemType == itemTypeDiscount:
			ok, err := r.addDiscount(path, it, raw.Item.Items[i], amount, prev, prevLine, country)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if ok {
				addBase(path, it, amount)
			}
		case slices.Contains(itemTypesIgnored, itemType):
			// nothing to do
		case itemType == itemTypeDescription:
			r.notice(path, "description line not converted")
		default:
			// Markup and payment lines modify the amounts of other lines, so
			// skipping them will result in different totals.
			r.notice(path, "unsupported item type %q", itemType)
		}
		prev, prevLine = nil, nil
	}

	return r.addHeaderDiscount(bases, country)
}

func (r *Result) newLine(path string, it *TransactionItem, amount num.Amount, country l10n.TaxCountryCode) (*bill.Line, error) {
	line := &bill.Line{
		Item: &org.Item{
			Name: firstOf(it.Description, refName(it.Item)),
		},
	}

	// NetSuite allows lines with only an amount, so fall back to a quantity
	// of one priced at the amount.
	if it.Quantity == "" || it.Rate == "" {
		line.Quantity = num.MakeAmount(1, 0)
		line.Item.Price = &amount
	} else {
		var err error
		if line.Quantity, err = parseAmount(it.Quantity); err != nil {
			return nil, fmt.Errorf("quantity: %w", err)
		}
		price, err := parseAmount(it.Rate)
		if err != nil {
			return nil, fmt.Errorf("rate: %w", err)
		}
		line.Item.Price = &price
	}

	combo, err := r.newTaxCombo(path, it, country)
	if err != nil {
		return nil, err
	}
	if combo != nil {
		line.Taxes = tax.Set{combo}
	}
	return line, nil
}

// addDiscount converts a discount line. NetSuite applies a discount line to
// the line directly before it, taxed with the discount line's own tax code.
// When that code matches the previous line's, it becomes a discount on that
// line, otherwise, such as after a subtotal, it becomes an invoice discount
// with the discount line's tax. Returns true if the discount was converted.
func (r *Result) addDiscount(path string, it *TransactionItem, src json.RawMessage, amount num.Amount, prev *TransactionItem, prevLine *bill.Line, country l10n.TaxCountryCode) (bool, error) {
	if !amount.IsNegative() {
		r.notice(path, "discount line with a positive amount not supported")
		return false, nil
	}
	reason := firstOf(it.Description, refName(it.Item))
	amount = amount.Negate()

	if prev != nil && sameTaxCode(prev, it) {
		d := &bill.LineDiscount{
			Reason: reason,
			Amount: amount,
		}
		prevLine.Discounts = append(prevLine.Discounts, d)
		r.track(ScopeLineDiscount, d, src)
		return true, nil
	}

	if it.TaxCode == nil || it.TaxCode.ID == "" {
		// Discounts applied after tax have no tax code.
		r.notice(path, "discount line without a tax code not supported")
		return false, nil
	}
	combo, err := r.newTaxCombo(path, it, country)
	if err != nil {
		return false, err
	}
	d := &bill.Discount{
		Reason: reason,
		Amount: amount,
		Taxes:  tax.Set{combo},
	}
	r.Invoice.Discounts = append(r.Invoice.Discounts, d)
	r.track(ScopeDiscount, d, src)
	return true, nil
}

// addHeaderDiscount converts the invoice's header discount. NetSuite shares
// it between tax codes in proportion to their net amounts, so one GOBL
// discount is added per tax code, with the last absorbing any rounding.
func (r *Result) addHeaderDiscount(bases []*taxBase, country l10n.TaxCountryCode) error {
	src := r.records.transaction
	total, err := r.money(src.DiscountTotal)
	if err != nil {
		return fmt.Errorf("invoice discountTotal: %w", err)
	}
	if total.IsZero() {
		return nil
	}
	if !total.IsNegative() {
		r.notice("transaction.discountTotal", "positive header discount not supported")
		return nil
	}
	total = total.Negate()

	exp := r.Invoice.Currency.Def().Subunits
	sum := num.MakeAmount(0, exp)
	for _, b := range bases {
		sum = sum.Add(b.amount)
	}
	if !sum.IsPositive() {
		r.notice("transaction.discountTotal", "no taxed lines to share the header discount between")
		return nil
	}

	reason := firstOf(refName(src.DiscountItem), "Discount")
	r.headerDiscountRates = len(bases)
	remaining := total
	for i, b := range bases {
		share := remaining
		if i < len(bases)-1 {
			share = total.Upscale(4).Multiply(b.amount).Divide(sum).Rescale(exp)
			remaining = remaining.Subtract(share)
		}
		combo, err := r.newTaxCombo(b.path, b.item, country)
		if err != nil {
			return err
		}
		d := &bill.Discount{
			Reason: reason,
			Amount: share,
			Taxes:  tax.Set{combo},
		}
		r.Invoice.Discounts = append(r.Invoice.Discounts, d)
		r.track(ScopeDiscount, d, r.Source.Transaction)
	}
	return nil
}

// newTaxCombo determines the line's tax from the mapping's entry for its tax
// code, such as the default preset's, which uses the tax code's properties.
// Without an entry, the line is taxed at its rate.
func (r *Result) newTaxCombo(path string, it *TransactionItem, country l10n.TaxCountryCode) (*tax.Combo, error) {
	if it.TaxCode == nil || it.TaxCode.ID == "" {
		r.notice(path, "line has no tax code")
		return nil, nil
	}
	tc, ok := r.records.taxCodes[it.TaxCode.ID]
	if !ok {
		return nil, fmt.Errorf("tax code %s not in bundle", it.TaxCode.ID)
	}

	var combo *tax.Combo
	m := r.Mapping.taxCode(tc, r.records.taxCodeFields[tc.ID])
	if m != nil && m.Reject != "" {
		return nil, fmt.Errorf("tax code %s (%s) rejected: %s", tc.ID, tc.ItemID, m.Reject)
	}
	if m != nil {
		c := *m.Combo
		c.Ext = m.Combo.Ext.Clone()
		combo = &c
	} else {
		combo = new(tax.Combo)
		if len(r.Mapping.TaxCodes) > 0 {
			r.notice(path+".taxCode", "tax code %s (%s) not in mapping, converted at the line's rate", tc.ID, tc.ItemID)
		}
	}

	if combo.Category == "" {
		if tc.TaxType == nil || tc.TaxType.RefName == "" {
			return nil, fmt.Errorf("tax code %s: missing tax type", tc.ID)
		}
		combo.Category = cbc.Code(strings.ToUpper(tc.TaxType.RefName))
	}
	if combo.Country == "" && tc.NexusCountry != nil {
		if c := l10n.TaxCountryCode(tc.NexusCountry.ID); c != "" && c != country {
			combo.Country = c
		}
	}

	// Taxable combos without a rate or percent take the line's percent, as
	// the line's rate may differ from the tax code's default.
	if combo.Percent == nil && combo.Rate == "" && (combo.Key == "" || combo.Key == tax.KeyStandard) {
		rate := firstOf(it.TaxRate1.String(), tc.Rate.String())
		p, err := num.PercentageFromString(rate + "%")
		if err != nil {
			return nil, fmt.Errorf("tax rate %q: %w", rate, err)
		}
		if p.IsZero() && combo.Key == "" {
			combo.Key = tax.KeyZero
		} else {
			combo.Percent = &p
		}
	}
	return combo, nil
}

func sameTaxCode(a, b *TransactionItem) bool {
	return a.TaxCode != nil && b.TaxCode != nil && a.TaxCode.ID == b.TaxCode.ID
}

func refName(ref *Ref) string {
	if ref == nil {
		return ""
	}
	return ref.RefName
}
