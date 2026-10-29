package netsuite

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/invopop/gobl/cbc"
	"github.com/invopop/gobl/l10n"
	"github.com/invopop/gobl/tax"
)

// Direction is whether a transaction records a sale or a purchase, which
// limits the tax codes it may use.
type Direction string

// Directions of transactions.
const (
	DirectionSale     Direction = "sale"
	DirectionPurchase Direction = "purchase"
)

// TaxCodeIndex finds the NetSuite tax code for a GOBL tax combo, the reverse
// of the mapping used to convert NetSuite transactions.
type TaxCodeIndex struct {
	codes []*indexedTaxCode
}

type indexedTaxCode struct {
	item *SalesTaxItem
	// available is NetSuite's "Available On": Both, Sale or Purchase.
	available string
	// nexus is the country the tax code is for, if any.
	nexus l10n.TaxCountryCode
	// combo is what the tax code converts to, relative to its own nexus.
	combo *tax.Combo
}

// NewTaxCodeIndex prepares the account's sales tax item records for reverse
// lookups with the mapping. Inactive codes, and codes the mapping rejects or
// that can't be converted, are left out.
func NewTaxCodeIndex(m *Mapping, records []json.RawMessage) (*TaxCodeIndex, error) {
	x := new(TaxCodeIndex)
	for _, rec := range records {
		tc := new(SalesTaxItem)
		if err := json.Unmarshal(rec, tc); err != nil {
			return nil, fmt.Errorf("parsing tax code: %w", err)
		}
		fields := make(map[string]any)
		if err := json.Unmarshal(rec, &fields); err != nil {
			return nil, fmt.Errorf("parsing tax code: %w", err)
		}
		if inactive, _ := fields["isInactive"].(bool); inactive {
			continue
		}
		var nexus l10n.TaxCountryCode
		if tc.NexusCountry != nil {
			nexus = l10n.TaxCountryCode(tc.NexusCountry.ID)
		}
		combo, e, err := m.taxComboFor(tc, fields, tc.Rate.String(), nexus)
		if err != nil || (e != nil && e.Disabled) {
			continue
		}
		x.codes = append(x.codes, &indexedTaxCode{item: tc, available: availableOn(fields["available"]), nexus: nexus, combo: combo})
	}
	return x, nil
}

// Find provides the tax code to record a combo with, in a transaction whose
// tax is accounted in the country given, such as the subsidiary's. Codes
// matching more of the combo's extensions are preferred, then codes only
// available in the direction, then the lowest internal ID. An intra-community
// supply is recorded by the buyer with a reverse charge code when there's no
// intra-community one, as the buyer accounts for the tax.
func (x *TaxCodeIndex) Find(c *tax.Combo, country l10n.TaxCountryCode, dir Direction) (*SalesTaxItem, error) {
	if c == nil {
		return nil, fmt.Errorf("missing tax combo")
	}
	code, err := x.find(c, country, dir)
	if err != nil && dir == DirectionPurchase && c.Key == tax.KeyIntraCommunity {
		rc := *c
		rc.Key = tax.KeyReverseCharge
		if code, rcErr := x.find(&rc, country, dir); rcErr == nil {
			return code, nil
		}
	}
	return code, err
}

func (x *TaxCodeIndex) find(c *tax.Combo, country l10n.TaxCountryCode, dir Direction) (*SalesTaxItem, error) {
	target := country
	if c.Country != "" {
		target = c.Country
	}
	type candidate struct {
		code  *indexedTaxCode
		score int
	}
	var found []candidate
	for _, code := range x.codes {
		if !code.availableFor(dir) || (code.nexus != "" && code.nexus != target) {
			continue
		}
		if score, ok := code.matches(c); ok {
			found = append(found, candidate{code, score})
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no %s tax code for %s", dir, describeCombo(c, target))
	}
	slices.SortStableFunc(found, func(a, b candidate) int {
		if a.score != b.score {
			return b.score - a.score
		}
		if sa, sb := a.code.specific(), b.code.specific(); sa != sb {
			if sa {
				return -1
			}
			return 1
		}
		return compareIDs(a.code.item.ID, b.code.item.ID)
	})
	return found[0].code.item, nil
}

// availableOn reads a tax code's "Available On", a list value such as
// {"id": "PURCHASE"}, or its name.
func availableOn(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case map[string]any:
		if id, ok := v["id"].(string); ok {
			return id
		}
	}
	return ""
}

func (c *indexedTaxCode) availableFor(dir Direction) bool {
	switch strings.ToLower(c.available) {
	case "", "both":
		return true
	case "sale":
		return dir == DirectionSale
	case "purchase":
		return dir == DirectionPurchase
	}
	return false
}

// specific is true for codes available in only one direction.
func (c *indexedTaxCode) specific() bool {
	a := strings.ToLower(c.available)
	return a == "sale" || a == "purchase"
}

// matches compares the tax code's combo with one to record: the category,
// the key, the percent for standard rates, and extensions both define, which
// score a point each.
func (c *indexedTaxCode) matches(t *tax.Combo) (int, bool) {
	if c.combo.Category != t.Category || normalKey(c.combo.Key) != normalKey(t.Key) {
		return 0, false
	}
	// Only taxed keys have a meaningful percent: GOBL may give others 0%.
	if normalKey(t.Key) == tax.KeyStandard {
		p := c.combo.Percent
		if p == nil || t.Percent == nil || !p.Equals(*t.Percent) {
			return 0, false
		}
	}
	score := 0
	for k, v := range c.combo.Ext.All() {
		if !t.Ext.Has(k) {
			continue
		}
		if t.Ext.Get(k) != v {
			return 0, false
		}
		score++
	}
	return score, true
}

func normalKey(k cbc.Key) cbc.Key {
	if k == "" {
		return tax.KeyStandard
	}
	return k
}

func describeCombo(c *tax.Combo, country l10n.TaxCountryCode) string {
	parts := []string{string(c.Category), string(normalKey(c.Key))}
	if c.Percent != nil {
		parts = append(parts, c.Percent.String())
	}
	if country != "" {
		parts = append(parts, "in "+string(country))
	}
	return strings.Join(parts, " ")
}

// compareIDs orders NetSuite internal IDs numerically.
func compareIDs(a, b string) int {
	ia, ea := strconv.Atoi(a)
	ib, eb := strconv.Atoi(b)
	if ea == nil && eb == nil {
		return ia - ib
	}
	return strings.Compare(a, b)
}
