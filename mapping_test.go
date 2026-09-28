package netsuite_test

import (
	"encoding/json"
	"testing"
	"time"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl/tax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mapping(t *testing.T, data string) *netsuite.Mapping {
	t.Helper()
	m := new(netsuite.Mapping)
	require.NoError(t, json.Unmarshal([]byte(data), m))
	return m
}

func TestMerge(t *testing.T) {
	base := mapping(t, `{
		"tax_codes": [
			{"code": "S-ES", "combo": {"cat": "VAT", "key": "standard"}},
			{"code": "EX-ES", "combo": {"cat": "VAT", "key": "exempt"}}
		],
		"rules": [
			{"id": "a", "jq": ". "},
			{"id": "b", "jq": ". "}
		]
	}`)
	override := mapping(t, `{
		"tax_codes": [
			{"code": "EX-ES", "combo": {"cat": "VAT", "key": "exempt", "ext": {"es-verifactu-exempt": "E6"}}},
			{"code": "S-ES", "disabled": true},
			{"id": "42", "combo": {"cat": "VAT", "key": "reverse-charge"}}
		],
		"rules": [
			{"id": "b", "jq": ".code = \"B\""},
			{"id": "a", "disabled": true},
			{"id": "c", "jq": ". "}
		]
	}`)

	m := netsuite.Merge(base, nil, override)
	require.Len(t, m.TaxCodes, 2)
	assert.Equal(t, "EX-ES", m.TaxCodes[0].Code)
	assert.Equal(t, "E6", m.TaxCodes[0].Combo.Ext.Get("es-verifactu-exempt").String())
	assert.Equal(t, "42", m.TaxCodes[1].ID)
	require.Len(t, m.Rules, 2)
	assert.Equal(t, "b", m.Rules[0].ID)
	assert.Equal(t, `.code = "B"`, m.Rules[0].JQ)
	assert.Equal(t, "c", m.Rules[1].ID)
	assert.NoError(t, m.Validate())
}

func TestMappingValidate(t *testing.T) {
	m := mapping(t, `{
		"tax_codes": [
			{"combo": {"cat": "VAT"}},
			{"code": "S-ES"}
		],
		"rules": [
			{"jq": "."},
			{"id": "x", "jq": "."},
			{"id": "x", "jq": "."},
			{"id": "y", "scope": "address", "jq": "."},
			{"id": "z", "jq": ".foo ="}
		]
	}`)
	err := m.Validate()
	require.Error(t, err)
	for _, msg := range []string{
		"tax_codes[0]: id or code required",
		"tax_codes[1]: combo required",
		"rules[0]: id required",
		`rules[2]: duplicate id "x"`,
		`rules[3]: unknown scope "address"`,
		"rules[4]: parsing jq",
	} {
		assert.ErrorContains(t, err, msg)
	}
}

func TestPresets(t *testing.T) {
	assert.Contains(t, netsuite.Presets(), "es")
	for _, name := range netsuite.Presets() {
		t.Run(name, func(t *testing.T) {
			m, err := netsuite.Preset(name)
			require.NoError(t, err)
			assert.NoError(t, m.Validate())
		})
	}
	_, err := netsuite.Preset("xx")
	assert.ErrorIs(t, err, netsuite.ErrUnknownPreset)
}

func TestTaxCodeMapping(t *testing.T) {
	t.Run("default preset from the supplier's country", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t))
		require.NoError(t, err)
		require.NotNil(t, res.Mapping)
		assert.NotEmpty(t, res.Mapping.TaxCodes)
	})

	t.Run("id takes priority over code", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t), netsuite.WithMapping(mapping(t, `{
			"tax_codes": [{"id": "6", "combo": {"cat": "VAT", "key": "standard", "ext": {"es-verifactu-regime": "03"}}}]
		}`)))
		require.NoError(t, err)
		combo := res.Invoice.Lines[0].Taxes[0]
		assert.Equal(t, "03", combo.Ext.Get("es-verifactu-regime").String())
		assert.Equal(t, "21%", combo.Percent.String(), "percent from the line")
	})

	t.Run("mapped percent", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t), netsuite.WithMapping(mapping(t, `{
			"tax_codes": [{"id": "6", "combo": {"cat": "VAT", "percent": "10%"}}]
		}`)))
		require.NoError(t, err)
		assert.Equal(t, "10%", res.Invoice.Lines[0].Taxes[0].Percent.String())
	})

	t.Run("category from tax type", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t), netsuite.WithMapping(mapping(t, `{
			"tax_codes": [{"code": "S-ES", "combo": {"key": "standard"}}]
		}`)))
		require.NoError(t, err)
		assert.Equal(t, tax.CategoryVAT, res.Invoice.Lines[0].Taxes[0].Category)
	})

	t.Run("codes missing from mapping", func(t *testing.T) {
		b := loadBundle(t, "examples/netsuite/invoice_mixed_rates.json")
		res, err := netsuite.FromInvoice(b, netsuite.WithPresets(), netsuite.WithMapping(mapping(t, `{
			"tax_codes": [{"code": "S-ES", "combo": {"cat": "VAT", "key": "standard"}}]
		}`)))
		require.NoError(t, err)
		require.Len(t, res.Unmapped, 4)
		assert.Equal(t, "transaction.item.items[1].taxCode", res.Unmapped[0].Path)
		assert.Equal(t, "tax code 7 (R-ES) not in mapping, converted from its flags", res.Unmapped[0].Message)
	})

	t.Run("no notices without a mapping", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t), netsuite.WithPresets())
		require.NoError(t, err)
		assert.Empty(t, res.Unmapped)
	})

	t.Run("invalid mapping", func(t *testing.T) {
		_, err := netsuite.FromInvoice(basicBundle(t), netsuite.WithMapping(mapping(t, `{
			"rules": [{"id": "x", "jq": ".foo ="}]
		}`)))
		assert.ErrorContains(t, err, "mapping: rules[0]: parsing jq")
	})
}

func withRules(t *testing.T, rules string) netsuite.Option {
	t.Helper()
	return netsuite.WithMapping(mapping(t, `{"rules": `+rules+`}`))
}

func TestRules(t *testing.T) {
	t.Run("document", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) { m["custbody_po"] = "PO-9" })
		res, err := netsuite.FromInvoice(b, withRules(t, `[
			{"id": "po", "jq": ".ordering.code = $source.transaction.custbody_po"}
		]`))
		require.NoError(t, err)
		assert.Equal(t, "PO-9", res.Invoice.Ordering.Code.String())
		assert.Empty(t, res.Unmapped, "custom field used by a rule is mapped")
	})

	t.Run("document rules may remove elements", func(t *testing.T) {
		res, err := netsuite.FromInvoice(loadBundle(t, "examples/netsuite/invoice_mixed_rates.json"), withRules(t, `[
			{"id": "drop", "jq": ".lines |= .[:2]"}
		]`))
		require.NoError(t, err)
		assert.Len(t, res.Invoice.Lines, 2)
	})

	t.Run("line", func(t *testing.T) {
		b := loadBundle(t, "examples/netsuite/invoice_mixed_rates.json")
		modify(t, &b.Transaction, func(m map[string]any) {
			lines(m)[2].(map[string]any)["custcol_cn_code"] = "8471"
		})
		res, err := netsuite.FromInvoice(b, withRules(t, `[
			{"id": "ref", "scope": "line", "jq": ".item.ref = $src.item.id"},
			{"id": "cn", "scope": "line", "jq": "if $src.custcol_cn_code then .item.identities += [{type: \"CN\", code: $src.custcol_cn_code}] end"}
		]`))
		require.NoError(t, err)
		refs := make([]string, len(res.Invoice.Lines))
		for i, l := range res.Invoice.Lines {
			refs[i] = l.Item.Ref.String()
		}
		assert.Equal(t, []string{"14", "15", "16", "15", "14"}, refs)
		assert.Empty(t, res.Invoice.Lines[0].Item.Identities)
		require.Len(t, res.Invoice.Lines[2].Item.Identities, 1)
		assert.Equal(t, "8471", res.Invoice.Lines[2].Item.Identities[0].Code.String())
		assert.Empty(t, res.Unmapped)
	})

	t.Run("line discount", func(t *testing.T) {
		res, err := netsuite.FromInvoice(loadBundle(t, "examples/netsuite/invoice_line_discount.json"), withRules(t, `[
			{"id": "d", "scope": "line-discount", "jq": ".code = $src.itemType.id"}
		]`))
		require.NoError(t, err)
		assert.Equal(t, "Discount", res.Invoice.Lines[0].Discounts[0].Code.String())
	})

	t.Run("discount", func(t *testing.T) {
		res, err := netsuite.FromInvoice(loadBundle(t, "examples/netsuite/invoice_subtotal_discount.json"), withRules(t, `[
			{"id": "d", "scope": "discount", "jq": ".code = $src.itemType.id"}
		]`))
		require.NoError(t, err)
		assert.Equal(t, "Discount", res.Invoice.Discounts[0].Code.String())
	})

	t.Run("header discount", func(t *testing.T) {
		res, err := netsuite.FromInvoice(loadBundle(t, "examples/netsuite/invoice_header_discount_rates.json"), withRules(t, `[
			{"id": "d", "scope": "discount", "jq": ".reason = \"Header \" + $src.tranId"}
		]`))
		require.NoError(t, err)
		require.Len(t, res.Invoice.Discounts, 2)
		for _, d := range res.Invoice.Discounts {
			assert.Equal(t, "Header 11", d.Reason)
		}
	})

	t.Run("parties", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t), withRules(t, `[
			{"id": "c", "scope": "customer", "jq": ".alias = $src.entityId"},
			{"id": "s", "scope": "supplier", "jq": ".alias = ($src.name + \" (\" + $scope + \")\")"}
		]`))
		require.NoError(t, err)
		assert.Equal(t, "1 Telefónica de España, S.A.U.", res.Invoice.Customer.Alias)
		assert.Equal(t, "Invopop SL (supplier)", res.Invoice.Supplier.Alias)
	})

	t.Run("preceding", func(t *testing.T) {
		res, err := netsuite.FromCreditMemo(loadBundle(t, "examples/netsuite/creditmemo_applied.json"), withRules(t, `[
			{"id": "p", "scope": "preceding", "jq": ".reason = \"Credits invoice \" + $src.tranId"}
		]`))
		require.NoError(t, err)
		assert.Equal(t, "Credits invoice 4", res.Invoice.Preceding[0].Reason)
	})

	t.Run("no access to the environment", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t), withRules(t, `[
			{"id": "env", "jq": ".ordering.code = ($ENV | length | tostring)"}
		]`))
		require.NoError(t, err)
		assert.Equal(t, "0", res.Invoice.Ordering.Code.String())
	})
}

func TestRuleErrors(t *testing.T) {
	tests := []struct {
		name, rules, err string
	}{
		{"no output", `[{"id": "x", "jq": "empty"}]`, `rule "x": no output`},
		{"several outputs", `[{"id": "x", "jq": ". , ."}]`, `rule "x": more than one output`},
		{"not an object", `[{"id": "x", "scope": "line", "jq": "1"}]`, `rule "x": line 0: output must be an object, got number`},
		{"runtime error", `[{"id": "x", "jq": "error(\"boom\")"}]`, `rule "x": error: boom`},
		{"invalid document", `[{"id": "x", "jq": ".lines = \"none\""}]`, `rule "x": decoding output`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := netsuite.FromInvoice(basicBundle(t), withRules(t, tt.rules))
			assert.ErrorContains(t, err, tt.err)
		})
	}

	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := netsuite.FromInvoice(basicBundle(t),
			withRules(t, `[{"id": "x", "jq": "last(range(1e15)) as $x | ."}]`),
			netsuite.WithRuleTimeout(50*time.Millisecond))
		assert.ErrorContains(t, err, "context deadline exceeded")
		assert.Less(t, time.Since(start), 2*time.Second)
	})
}
