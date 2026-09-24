package goblnetsuite

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

// Item types, as used in the itemType field of invoice lines. Only
// "Service" has been seen in responses so far; the others are the standard
// NetSuite item type IDs.
const (
	itemTypeAssembly    = "Assembly"
	itemTypeDescription = "Description"
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

// setLines converts the invoice's item sublist into GOBL lines.
func (r *Result) setLines(country l10n.TaxCountryCode) error {
	src := r.records.invoice
	if src.Item == nil {
		return nil
	}
	var raw struct {
		Item struct {
			Items []json.RawMessage `json:"items"`
		} `json:"item"`
	}
	if err := json.Unmarshal(r.Source.Invoice, &raw); err != nil {
		return fmt.Errorf("parsing invoice lines: %w", err)
	}

	for i, it := range src.Item.Items {
		path := fmt.Sprintf("invoice.item.items[%d]", i)
		itemType := ""
		if it.ItemType != nil {
			itemType = it.ItemType.ID
		}
		switch {
		case slices.Contains(itemTypesSold, itemType):
			line, err := r.newLine(path, it, country)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			r.Invoice.Lines = append(r.Invoice.Lines, line)
			r.SourceLines = append(r.SourceLines, raw.Item.Items[i])
		case slices.Contains(itemTypesIgnored, itemType):
			continue
		case itemType == itemTypeDescription:
			r.notice(path, "description line not converted")
		default:
			// Discount, markup and payment lines modify the amounts of other
			// lines, so skipping them will result in different totals.
			r.notice(path, "unsupported item type %q", itemType)
		}
	}
	return nil
}

func (r *Result) newLine(path string, it *InvoiceItem, country l10n.TaxCountryCode) (*bill.Line, error) {
	line := &bill.Line{
		Item: &org.Item{
			Name: firstOf(it.Description, refName(it.Item)),
		},
	}

	amount, err := parseAmount(it.Amount)
	if err != nil {
		return nil, fmt.Errorf("amount: %w", err)
	}
	// NetSuite allows lines with only an amount, so fall back to a quantity
	// of one priced at the amount.
	if it.Quantity == "" || it.Rate == "" {
		line.Quantity = num.MakeAmount(1, 0)
		line.Item.Price = &amount
	} else {
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

// newTaxCombo determines the line's tax from its tax code, which in legacy
// tax accounts describes the type of tax and how it applies.
func (r *Result) newTaxCombo(path string, it *InvoiceItem, country l10n.TaxCountryCode) (*tax.Combo, error) {
	if it.TaxCode == nil || it.TaxCode.ID == "" {
		r.notice(path, "line has no tax code")
		return nil, nil
	}
	tc, ok := r.records.taxCodes[it.TaxCode.ID]
	if !ok {
		return nil, fmt.Errorf("tax code %s not in bundle", it.TaxCode.ID)
	}
	if tc.TaxType == nil || tc.TaxType.RefName == "" {
		return nil, fmt.Errorf("tax code %s: missing tax type", tc.ID)
	}

	combo := &tax.Combo{
		Category: cbc.Code(strings.ToUpper(tc.TaxType.RefName)),
	}
	if tc.NexusCountry != nil {
		if c := l10n.TaxCountryCode(tc.NexusCountry.ID); c != "" && c != country {
			combo.Country = c
		}
	}

	switch {
	case tc.ReverseCharge:
		combo.Key = tax.KeyReverseCharge
	case tc.ECCode:
		combo.Key = tax.KeyIntraCommunity
	case tc.Export:
		combo.Key = tax.KeyExport
	case tc.Exempt:
		combo.Key = tax.KeyExempt
	default:
		// The line's rate may differ from the tax code's default.
		rate := firstOf(it.TaxRate1.String(), tc.Rate.String())
		p, err := num.PercentageFromString(rate + "%")
		if err != nil {
			return nil, fmt.Errorf("tax rate %q: %w", rate, err)
		}
		if p.IsZero() {
			combo.Key = tax.KeyZero
		} else {
			combo.Percent = &p
		}
	}
	return combo, nil
}

func refName(ref *Ref) string {
	if ref == nil {
		return ""
	}
	return ref.RefName
}
