package netsuite_test

import (
	"testing"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendRules(t *testing.T) {
	inv := purchase(t, func(doc map[string]any) {
		doc["ordering"] = map[string]any{"code": "PO-77"}
	})

	t.Run("lines then document", func(t *testing.T) {
		o := purchaseOptions(t)
		o.Mapping = &netsuite.Mapping{Rules: []*netsuite.Rule{
			{ID: "fibre-account", Direction: netsuite.RuleSend, Scope: netsuite.ScopeLine, JQ: `if ($src.item.name // "") | test("Fibre") then .account = {id: "131"} else . end`},
			{ID: "department", Direction: netsuite.RuleSend, Scope: netsuite.ScopeLine, JQ: `.department = {id: "4"}`},
			{ID: "po", Direction: netsuite.RuleSend, JQ: `.custbody_po = $src.ordering.code | .memo = "Lines: \(.expense.items | length)"`},
			{ID: "receive-only", JQ: `.supplier.name = "ignored"`},
		}}
		out, err := netsuite.NewPurchase(inv, o)
		require.NoError(t, err)
		lines := expenseLines(t, out)
		require.Len(t, lines, 4)
		assert.Equal(t, map[string]any{"id": "133"}, lines[0]["account"])
		assert.Equal(t, map[string]any{"id": "131"}, lines[2]["account"], "Fibre 1Gb")
		assert.Equal(t, map[string]any{"id": "133"}, lines[3]["account"], "the discount has no item")
		for _, l := range lines {
			assert.Equal(t, map[string]any{"id": "4"}, l["department"])
		}
		assert.Equal(t, "PO-77", out.Body["custbody_po"])
		assert.Equal(t, "Lines: 4", out.Body["memo"], "document rules see the lines")
		assert.NotContains(t, out.Body, "supplier")
		assert.Equal(t, "se1", out.Body["externalId"])
	})

	t.Run("the external ID is kept", func(t *testing.T) {
		o := purchaseOptions(t)
		o.Mapping = &netsuite.Mapping{Rules: []*netsuite.Rule{
			{ID: "eid", Direction: netsuite.RuleSend, JQ: `.externalId = "other"`},
		}}
		_, err := netsuite.NewPurchase(inv, o)
		assert.ErrorContains(t, err, "rules can't change the externalId")
	})

	t.Run("errors name the rule", func(t *testing.T) {
		o := purchaseOptions(t)
		o.Mapping = &netsuite.Mapping{Rules: []*netsuite.Rule{
			{ID: "bad", Direction: netsuite.RuleSend, Scope: netsuite.ScopeLine, JQ: `"text"`},
		}}
		_, err := netsuite.NewPurchase(inv, o)
		assert.ErrorContains(t, err, `rule "bad": line 0: output must be an object`)
	})

	t.Run("vendors", func(t *testing.T) {
		out, err := netsuite.NewVendor(orange(), &netsuite.VendorOptions{Subsidiary: "3", Mapping: &netsuite.Mapping{Rules: []*netsuite.Rule{
			{ID: "category", Direction: netsuite.RuleSend, Scope: netsuite.ScopeVendor, JQ: `.category = {id: "3"} | .emailTransactions = ($src.emails | length > 0)`},
			{ID: "bill-only", Direction: netsuite.RuleSend, JQ: `.memo = "not a vendor"`},
		}}})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"id": "3"}, out.Body["category"])
		assert.Equal(t, true, out.Body["emailTransactions"])
		assert.NotContains(t, out.Body, "memo", "document rules are for transactions")
	})
}

func TestValidateSendRules(t *testing.T) {
	m := &netsuite.Mapping{Rules: []*netsuite.Rule{
		{ID: "ok", Direction: netsuite.RuleSend, Scope: netsuite.ScopeVendor, JQ: `.`},
		{ID: "scope", Direction: netsuite.RuleSend, Scope: netsuite.ScopeCustomer, JQ: `.`},
		{ID: "direction", Direction: "sideways", JQ: `.`},
		{ID: "vendor-on-receive", Scope: netsuite.ScopeVendor, JQ: `.`},
	}}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `rules[1]: scope "customer" is not available when sending`)
	assert.Contains(t, err.Error(), `rules[2]: unknown direction "sideways"`)
	assert.Contains(t, err.Error(), `rules[3]: unknown scope "vendor"`)
	assert.NotContains(t, err.Error(), "rules[0]")
}

func TestReceiveIgnoresSendRules(t *testing.T) {
	b := loadBundle(t, "examples/netsuite/invoice_basic.json")
	_, err := netsuite.Convert(b, netsuite.WithMapping(&netsuite.Mapping{Rules: []*netsuite.Rule{
		{ID: "bad", Direction: netsuite.RuleSend, JQ: `"would fail"`},
	}}))
	require.NoError(t, err)
}
