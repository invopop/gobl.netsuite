package netsuite

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/invopop/gobl/tax"
	"github.com/itchyny/gojq"
)

// Mapping customises how NetSuite transactions are converted. Presets provide
// mappings for regions, which accounts can extend or override with their
// own, as mappings are merged in order with Merge.
type Mapping struct {
	// TaxCodes map NetSuite tax codes to GOBL tax combos. Codes without an
	// entry are converted as taxable at the line's rate, and reported.
	TaxCodes []*TaxCodeMap `json:"tax_codes,omitempty"`

	// Rules modify the converted document using jq, for example to set
	// data from custom fields.
	Rules []*Rule `json:"rules,omitempty"`
}

// TaxCodeMap maps NetSuite tax codes to a GOBL tax combo. Each entry
// matches tax codes in one way, in order of priority:
//
//   - ID: the internal ID of a tax code, specific to an account.
//   - Code: the name of a tax code, e.g. "S-ES", which accounts may change.
//   - Match: the properties of tax codes, as used by presets, since NetSuite
//     identifies tax codes by their properties rather than their names.
//
// When several Match entries apply, the one with the most criteria is used,
// or if tied, the one defined last, so that later mappings take priority.
type TaxCodeMap struct {
	// ID is the internal ID of the tax code.
	ID string `json:"id,omitempty"`
	// Code is the name of the tax code, its itemId.
	Code string `json:"code,omitempty"`
	// Match lists fields of the tax code record and the values they must
	// have, where nested fields use dots, e.g. {"exempt": true,
	// "nexusCountry.id": "ES"}. Missing fields match false or null. An empty
	// match applies to all tax codes.
	Match map[string]any `json:"match,omitempty"`
	// Combo is used for lines with the tax code. When it provides neither a
	// percent nor a rate, and the key is standard or empty, the percent is
	// taken from the NetSuite line. The category defaults to the tax code's
	// tax type.
	Combo *tax.Combo `json:"combo,omitempty"`
	// Disabled removes an entry defined by an earlier mapping.
	Disabled bool `json:"disabled,omitempty"`
}

// key identifies the entry when merging mappings.
func (m *TaxCodeMap) key() string {
	switch {
	case m.ID != "":
		return "id:" + m.ID
	case m.Code != "":
		return "code:" + m.Code
	default:
		// Maps are encoded with sorted keys, so equal criteria give equal keys.
		data, _ := json.Marshal(m.Match)
		return "match:" + string(data)
	}
}

// matches checks the criteria against the tax code record.
func (m *TaxCodeMap) matches(record map[string]any) bool {
	for path, want := range m.Match {
		var v any = record
		for _, k := range strings.Split(path, ".") {
			obj, ok := v.(map[string]any)
			if !ok {
				v = nil
				break
			}
			v = obj[k]
		}
		if v == nil {
			if want != nil && want != false {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(v, want) {
			return false
		}
	}
	return true
}

// Scope determines what a rule is applied to, and so the data available.
type Scope string

// Scopes for rules. In each, "." is the GOBL element, and "$src" the
// NetSuite record or line it was converted from. "$source" is always the
// whole bundle.
const (
	// ScopeDocument rules apply to the whole GOBL invoice, after all other
	// scopes. $src is null. Only these rules may add or remove elements.
	ScopeDocument Scope = "document"
	// ScopeLine rules apply to each line, with $src the item line.
	ScopeLine Scope = "line"
	// ScopeLineDiscount rules apply to each line discount, with $src the
	// discount item line.
	ScopeLineDiscount Scope = "line-discount"
	// ScopeDiscount rules apply to each invoice discount, with $src the
	// discount item line, or the transaction for header discounts.
	ScopeDiscount Scope = "discount"
	// ScopeCustomer rules apply to the customer, with $src the customer
	// record.
	ScopeCustomer Scope = "customer"
	// ScopeSupplier rules apply to the supplier, with $src the subsidiary.
	ScopeSupplier Scope = "supplier"
	// ScopePreceding rules apply to each preceding document of a credit
	// note, with $src the related invoice.
	ScopePreceding Scope = "preceding"
)

// scopes lists the element scopes in the order they are applied, followed
// by the document scope.
var scopes = []Scope{
	ScopeSupplier,
	ScopeCustomer,
	ScopeLine,
	ScopeLineDiscount,
	ScopeDiscount,
	ScopePreceding,
	ScopeDocument,
}

// Rule is a jq program applied to part of the converted document, which
// must produce a single value to replace it.
type Rule struct {
	// ID identifies the rule when merging mappings.
	ID string `json:"id"`
	// Description explains what the rule does.
	Description string `json:"description,omitempty"`
	// Scope is what the rule applies to, the document by default.
	Scope Scope `json:"scope,omitempty"`
	// JQ is the program, e.g. `.ordering.code = $source.transaction.custbody_po`.
	JQ string `json:"jq,omitempty"`
	// Disabled removes a rule defined by an earlier mapping.
	Disabled bool `json:"disabled,omitempty"`
}

func (r *Rule) scope() Scope {
	if r.Scope == "" {
		return ScopeDocument
	}
	return r.Scope
}

// Merge combines mappings in order. Tax codes with the same ID or code, and
// rules with the same ID, replace those of earlier mappings, or remove them
// if disabled. Otherwise entries are appended. Nil mappings are ignored.
func Merge(mappings ...*Mapping) *Mapping {
	out := new(Mapping)
	for _, m := range mappings {
		if m == nil {
			continue
		}
		for _, tc := range m.TaxCodes {
			i := slices.IndexFunc(out.TaxCodes, func(e *TaxCodeMap) bool { return e.key() == tc.key() })
			switch {
			case i >= 0 && tc.Disabled:
				out.TaxCodes = slices.Delete(out.TaxCodes, i, i+1)
			case i >= 0:
				out.TaxCodes[i] = tc
			case !tc.Disabled:
				out.TaxCodes = append(out.TaxCodes, tc)
			}
		}
		for _, r := range m.Rules {
			i := slices.IndexFunc(out.Rules, func(e *Rule) bool { return e.ID == r.ID })
			switch {
			case i >= 0 && r.Disabled:
				out.Rules = slices.Delete(out.Rules, i, i+1)
			case i >= 0:
				out.Rules[i] = r
			case !r.Disabled:
				out.Rules = append(out.Rules, r)
			}
		}
	}
	return out
}

// Validate checks the mapping is complete and its rules compile, so that
// problems are found when a mapping is saved rather than when used.
func (m *Mapping) Validate() error {
	var errs []error
	for i, tc := range m.TaxCodes {
		n := 0
		for _, set := range []bool{tc.ID != "", tc.Code != "", tc.Match != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			errs = append(errs, fmt.Errorf("tax_codes[%d]: one of id, code or match required", i))
		}
		if tc.Combo == nil && !tc.Disabled {
			errs = append(errs, fmt.Errorf("tax_codes[%d]: combo required", i))
		}
	}
	ids := make(map[string]bool)
	for i, r := range m.Rules {
		if r.ID == "" {
			errs = append(errs, fmt.Errorf("rules[%d]: id required", i))
		} else if ids[r.ID] {
			errs = append(errs, fmt.Errorf("rules[%d]: duplicate id %q", i, r.ID))
		}
		ids[r.ID] = true
		if !slices.Contains(scopes, r.scope()) {
			errs = append(errs, fmt.Errorf("rules[%d]: unknown scope %q", i, r.Scope))
		}
		if r.Disabled {
			continue
		}
		if _, err := compileRule(r); err != nil {
			errs = append(errs, fmt.Errorf("rules[%d]: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

// taxCode finds the entry for a tax code, preferring matches by ID, then
// code, then the entry whose criteria match with the most fields, or the
// later one if tied.
func (m *Mapping) taxCode(tc *SalesTaxItem, record map[string]any) *TaxCodeMap {
	if m == nil {
		return nil
	}
	for _, e := range m.TaxCodes {
		if e.ID != "" && e.ID == tc.ID {
			return e
		}
	}
	for _, e := range m.TaxCodes {
		if e.Code != "" && e.Code == tc.ItemID {
			return e
		}
	}
	var best *TaxCodeMap
	for _, e := range m.TaxCodes {
		if e.Match == nil || !e.matches(record) {
			continue
		}
		if best == nil || len(e.Match) >= len(best.Match) {
			best = e
		}
	}
	return best
}

// refersTo returns true when a rule mentions the field name.
func (m *Mapping) refersTo(field string) bool {
	if m == nil {
		return false
	}
	for _, r := range m.Rules {
		if strings.Contains(r.JQ, field) {
			return true
		}
	}
	return false
}

// compileRule parses and compiles a rule's program. Programs have no access
// to the environment or modules, and receive the $source, $src and $scope
// variables.
func compileRule(r *Rule) (*gojq.Code, error) {
	q, err := gojq.Parse(r.JQ)
	if err != nil {
		return nil, fmt.Errorf("parsing jq: %w", err)
	}
	code, err := gojq.Compile(q, gojq.WithVariables(ruleVariables))
	if err != nil {
		return nil, fmt.Errorf("compiling jq: %w", err)
	}
	return code, nil
}

var ruleVariables = []string{"$source", "$src", "$scope"}
