package netsuite_test

import (
	"encoding/json"
	"testing"

	"github.com/invopop/gobl"
	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl/currency"
	"github.com/invopop/gobl/num"
	"github.com/invopop/gobl/tax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// basicBundle loads the basic invoice example.
func basicBundle(t *testing.T) *netsuite.Bundle {
	t.Helper()
	return loadBundle(t, "examples/netsuite/invoice_basic.json")
}

// modify decodes a raw record, applies fn to it, and encodes it again.
func modify(t *testing.T, raw *json.RawMessage, fn func(m map[string]any)) {
	t.Helper()
	m := make(map[string]any)
	require.NoError(t, json.Unmarshal(*raw, &m))
	fn(m)
	data, err := json.Marshal(m)
	require.NoError(t, err)
	*raw = data
}

// lines returns the item lines of a decoded invoice record.
func lines(m map[string]any) []any {
	return m["item"].(map[string]any)["items"].([]any)
}

func addAmounts(t *testing.T, a, b string) string {
	t.Helper()
	x, err := num.AmountFromString(a)
	require.NoError(t, err)
	y, err := num.AmountFromString(b)
	require.NoError(t, err)
	return x.Add(y).String()
}

func calculate(t *testing.T, res *netsuite.Result) {
	t.Helper()
	env, err := gobl.Envelop(res.Invoice)
	require.NoError(t, err)
	require.NoError(t, env.Calculate())
}

func TestFromInvoice(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t))
		require.NoError(t, err)
		inv := res.Invoice
		assert.Equal(t, "ES", inv.GetRegime().String())
		assert.Equal(t, "1", inv.Code.String())
		assert.Equal(t, "2026-09-24", inv.IssueDate.String())
		assert.Equal(t, currency.EUR, inv.Currency)
		assert.Empty(t, inv.ExchangeRates)
		assert.Equal(t, "Invopop SL", inv.Supplier.Name)
		assert.Equal(t, "B85905495", inv.Supplier.TaxID.Code.String())
		assert.Equal(t, "Telefónica de España, S.A.U.", inv.Customer.Name)
		require.Len(t, inv.Lines, 1)
		assert.Equal(t, "SVC-001 Consulting", inv.Lines[0].Item.Name)
		assert.Equal(t, "21%", inv.Lines[0].Taxes[0].Percent.String())
		assert.Empty(t, res.Unmapped)
	})

	t.Run("requires a subsidiary", func(t *testing.T) {
		b := basicBundle(t)
		b.Subsidiary = nil
		_, err := netsuite.FromInvoice(b)
		assert.ErrorContains(t, err, "subsidiary is required")
	})

	t.Run("requires tax codes in the bundle", func(t *testing.T) {
		b := basicBundle(t)
		b.TaxCodes = nil
		_, err := netsuite.FromInvoice(b)
		assert.ErrorContains(t, err, "tax code 6 not in bundle")
	})

	t.Run("foreign currency", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) {
			m["currency"] = map[string]any{"id": "2", "refName": "US Dollar"}
			m["exchangeRate"] = 0.9123
		})
		b.Currencies["2"] = json.RawMessage(`{"id":"2","name":"US Dollar","symbol":"USD"}`)
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		assert.Equal(t, currency.USD, res.Invoice.Currency)
		require.Len(t, res.Invoice.ExchangeRates, 1)
		er := res.Invoice.ExchangeRates[0]
		assert.Equal(t, currency.USD, er.From)
		assert.Equal(t, currency.EUR, er.To)
		assert.Equal(t, "0.9123", er.Amount.String())
	})

	t.Run("person customer", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Customer, func(m map[string]any) {
			m["isPerson"] = true
			m["firstName"] = "Ana"
			m["lastName"] = "García"
		})
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		assert.Equal(t, "Ana García", res.Invoice.Customer.Name)
	})

	t.Run("customer tax ID from customer record", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) { delete(m, "vatRegNum") })
		modify(t, &b.Customer, func(m map[string]any) { m["vatRegNumber"] = "ESB12345674" })
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		calculate(t, res)
		assert.Equal(t, "B12345674", res.Invoice.Customer.TaxID.Code.String())
	})

	t.Run("line without rate", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) {
			l := lines(m)[0].(map[string]any)
			delete(l, "rate")
			delete(l, "quantity")
		})
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		calculate(t, res)
		assert.Equal(t, "1", res.Invoice.Lines[0].Quantity.String())
		assert.Equal(t, "3000.00", res.Invoice.Lines[0].Item.Price.String())
		assert.NoError(t, res.CheckTotals())
	})

	t.Run("reference number", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) { m["otherRefNum"] = "PO-1234" })
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		assert.Equal(t, "PO-1234", res.Invoice.Ordering.Code.String())
	})
}

// TestFromInvoiceTaxKeys checks the conversion of tax codes from their flags,
// used when there is no mapping for the code, so without presets.
func TestFromInvoiceTaxKeys(t *testing.T) {
	tests := []struct {
		flag string
		key  string
	}{
		{"reverseCharge", tax.KeyReverseCharge.String()},
		{"ecCode", tax.KeyIntraCommunity.String()},
		{"export", tax.KeyExport.String()},
		{"exempt", tax.KeyExempt.String()},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			b := basicBundle(t)
			tc := b.TaxCodes["6"]
			modify(t, &tc, func(m map[string]any) {
				m[tt.flag] = true
				m["rate"] = 0
			})
			b.TaxCodes["6"] = tc
			res, err := netsuite.FromInvoice(b, netsuite.WithPresets())
			require.NoError(t, err)
			combo := res.Invoice.Lines[0].Taxes[0]
			assert.Equal(t, tt.key, combo.Key.String())
			assert.Nil(t, combo.Percent)
		})
	}

	t.Run("zero rate", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) {
			lines(m)[0].(map[string]any)["taxRate1"] = 0
		})
		res, err := netsuite.FromInvoice(b, netsuite.WithPresets())
		require.NoError(t, err)
		assert.Equal(t, tax.KeyZero, res.Invoice.Lines[0].Taxes[0].Key)
	})

	t.Run("line rate overrides tax code rate", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) {
			lines(m)[0].(map[string]any)["taxRate1"] = 10
		})
		res, err := netsuite.FromInvoice(b, netsuite.WithPresets())
		require.NoError(t, err)
		assert.Equal(t, "10%", res.Invoice.Lines[0].Taxes[0].Percent.String())
	})

	t.Run("line without tax code", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) {
			delete(lines(m)[0].(map[string]any), "taxCode")
		})
		res, err := netsuite.FromInvoice(b, netsuite.WithPresets())
		require.NoError(t, err)
		assert.Empty(t, res.Invoice.Lines[0].Taxes)
		require.Len(t, res.Unmapped, 1)
		assert.Equal(t, "transaction.item.items[0]", res.Unmapped[0].Path)
	})
}

func TestFromInvoiceLineTypes(t *testing.T) {
	b := basicBundle(t)
	modify(t, &b.Transaction, func(m map[string]any) {
		base := lines(m)[0].(map[string]any)
		typed := func(id string) map[string]any {
			return map[string]any{"itemType": map[string]any{"id": id}}
		}
		sub := typed("Subtotal")
		sub["amount"] = 3000
		desc := typed("Description")
		desc["description"] = "Delivered in September"
		markup := typed("Markup")
		markup["amount"] = 300
		disc := typed("Discount")
		disc["amount"] = -50
		m["item"].(map[string]any)["items"] = []any{base, sub, desc, markup, disc}
	})

	res, err := netsuite.FromInvoice(b)
	require.NoError(t, err)
	assert.Len(t, res.Invoice.Lines, 1)
	assert.Empty(t, res.Invoice.Discounts)
	require.Len(t, res.Unmapped, 3)
	assert.Equal(t, "transaction.item.items[2]", res.Unmapped[0].Path)
	assert.Equal(t, "description line not converted", res.Unmapped[0].Message)
	assert.Equal(t, "transaction.item.items[3]", res.Unmapped[1].Path)
	assert.Equal(t, `unsupported item type "Markup"`, res.Unmapped[1].Message)
	assert.Equal(t, "transaction.item.items[4]", res.Unmapped[2].Path)
	assert.Equal(t, "discount line without a tax code not supported", res.Unmapped[2].Message)
}

func TestFromInvoiceDiscounts(t *testing.T) {
	t.Run("line discount with a different tax code", func(t *testing.T) {
		b := loadBundle(t, "examples/netsuite/invoice_line_discount.json")
		b.TaxCodes["7"] = json.RawMessage(`{"id":"7","rate":10.0,"taxType":{"id":"1","refName":"VAT"}}`)
		modify(t, &b.Transaction, func(m map[string]any) {
			disc := lines(m)[1].(map[string]any)
			disc["taxCode"] = map[string]any{"id": "7"}
			disc["taxRate1"] = 10
		})
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		assert.Empty(t, res.Invoice.Lines[0].Discounts)
		require.Len(t, res.Invoice.Discounts, 1)
		d := res.Invoice.Discounts[0]
		assert.Equal(t, "333.33", d.Amount.String())
		assert.Equal(t, "10%", d.Taxes[0].Percent.String())
	})

	t.Run("header discount shared between tax codes", func(t *testing.T) {
		b := loadBundle(t, "examples/netsuite/invoice_mixed_rates.json")
		modify(t, &b.Transaction, func(m map[string]any) {
			m["discountItem"] = map[string]any{"id": "17", "refName": "Discount"}
			m["discountTotal"] = -100
		})
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		// One discount per tax code used by the lines, adding up exactly.
		require.Len(t, res.Invoice.Discounts, 5)
		amounts := make([]string, len(res.Invoice.Discounts))
		sum := "0.00"
		for i, d := range res.Invoice.Discounts {
			assert.Equal(t, "Discount", d.Reason)
			amounts[i] = d.Amount.String()
			sum = addAmounts(t, sum, amounts[i])
		}
		// Bases: 950.00, 99.99, 35.88, 250.00 and 240.00 of 1575.87.
		assert.Equal(t, []string{"60.28", "6.35", "2.28", "15.86", "15.23"}, amounts)
		assert.Equal(t, "100.00", sum)
	})
}

func TestFromInvoiceUnmapped(t *testing.T) {
	b := basicBundle(t)
	modify(t, &b.Transaction, func(m map[string]any) {
		m["custbody_project"] = map[string]any{"id": "7", "refName": "Alpha"}
		m["custbody_empty"] = ""
		m["shippingCost"] = 12.5
		lines(m)[0].(map[string]any)["custcol_cn_code"] = "8471"
	})

	res, err := netsuite.FromInvoice(b)
	require.NoError(t, err)
	paths := make([]string, len(res.Unmapped))
	for i, n := range res.Unmapped {
		paths[i] = n.Path
	}
	assert.ElementsMatch(t, []string{
		"transaction.shippingCost",
		"transaction.custbody_project",
		"transaction.item.items[0].custcol_cn_code",
	}, paths)
}

func TestCheckTotals(t *testing.T) {
	t.Run("header discount rounding", func(t *testing.T) {
		res, err := netsuite.FromInvoice(loadBundle(t, "examples/netsuite/invoice_header_discount_mixed.json"))
		require.NoError(t, err)
		calculate(t, res)
		require.NoError(t, res.CheckTotals())
		require.Len(t, res.Warnings, 2)
		assert.Equal(t, "transaction.taxTotal", res.Warnings[0].Path)
		assert.Contains(t, res.Warnings[0].Message, "netsuite 197.55, gobl 197.54")
	})

	t.Run("header discount rounding beyond tolerance", func(t *testing.T) {
		b := loadBundle(t, "examples/netsuite/invoice_header_discount_mixed.json")
		modify(t, &b.Transaction, func(m map[string]any) {
			// Five tax codes allow up to 0.05.
			m["taxTotal"] = 197.60
			m["total"] = 1673.47
		})
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		calculate(t, res)
		assert.ErrorContains(t, res.CheckTotals(), "taxTotal: netsuite 197.60, gobl 197.54")
	})

	t.Run("not calculated", func(t *testing.T) {
		res, err := netsuite.FromInvoice(basicBundle(t))
		require.NoError(t, err)
		assert.ErrorContains(t, res.CheckTotals(), "not been calculated")
	})

	t.Run("mismatch", func(t *testing.T) {
		b := basicBundle(t)
		modify(t, &b.Transaction, func(m map[string]any) {
			m["taxTotal"] = 600
			m["total"] = 3600
		})
		res, err := netsuite.FromInvoice(b)
		require.NoError(t, err)
		calculate(t, res)
		assert.EqualError(t, res.CheckTotals(),
			"totals differ from netsuite: taxTotal: netsuite 600.00, gobl 630.00; total: netsuite 3600.00, gobl 3630.00")
	})
}
