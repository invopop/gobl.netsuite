package netsuite_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/invopop/gobl"
	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl/bill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// purchase loads and calculates a GOBL invoice received from a supplier,
// changed by edit first, if given.
func purchase(t *testing.T, edit func(map[string]any)) *bill.Invoice {
	t.Helper()
	data, err := os.ReadFile("examples/gobl/purchase_es.json")
	require.NoError(t, err)
	if edit != nil {
		doc := make(map[string]any)
		require.NoError(t, json.Unmarshal(data, &doc))
		edit(doc)
		data, err = json.Marshal(doc)
		require.NoError(t, err)
	}
	inv := new(bill.Invoice)
	require.NoError(t, json.Unmarshal(data, inv))
	env, err := gobl.Envelop(inv)
	require.NoError(t, err)
	require.NoError(t, env.Calculate())
	require.NoError(t, env.Validate())
	return env.Extract().(*bill.Invoice)
}

func purchaseOptions(t *testing.T) *netsuite.PurchaseOptions {
	return &netsuite.PurchaseOptions{
		Subsidiary: "3", Country: "ES", Vendor: "8", Currency: "1", Account: "133",
		ExternalID: "se1", TaxCodes: testTaxCodes(t),
	}
}

func expenseLines(t *testing.T, out *netsuite.Outbound) []map[string]any {
	t.Helper()
	data, err := json.Marshal(out.Body["expense"])
	require.NoError(t, err)
	var sub struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(data, &sub))
	return sub.Items
}

func TestNewPurchase(t *testing.T) {
	t.Run("vendor bill", func(t *testing.T) {
		out, err := netsuite.NewPurchase(purchase(t, nil), purchaseOptions(t))
		require.NoError(t, err)
		assert.Equal(t, netsuite.RecordTypeVendorBill, out.RecordType)
		assert.Equal(t, "F2026-1043", out.Body["tranId"])
		assert.Equal(t, "2026-10-02", out.Body["tranDate"])
		assert.Equal(t, "2026-11-01", out.Body["dueDate"])
		assert.Equal(t, "se1", out.Body["externalId"])
		assert.Equal(t, "Mobile and fibre, September 2026", out.Body["memo"])
		assert.NotContains(t, out.Body, "approvalStatus")

		lines := expenseLines(t, out)
		require.Len(t, lines, 4)
		assert.Equal(t, map[string]any{"account": map[string]any{"id": "133"}, "amount": 99.99, "memo": "3 × Mobile line", "taxCode": map[string]any{"id": "6"}, "tax1Amt": 21.0}, lines[0])
		assert.Equal(t, "Loyalty discount", lines[3]["memo"])
		assert.Equal(t, -5.0, lines[3]["amount"])
		assert.Equal(t, -1.05, lines[3]["tax1Amt"])
	})

	t.Run("line taxes add up to the rate's total", func(t *testing.T) {
		inv := purchase(t, func(doc map[string]any) {
			line := map[string]any{"quantity": "1", "item": map[string]any{"name": "SIM", "price": "0.35"}, "taxes": []any{map[string]any{"cat": "VAT", "rate": "general"}}}
			doc["lines"] = []any{line, line, line}
			delete(doc, "discounts")
		})
		assert.Equal(t, "0.22", inv.Totals.Tax.String(), "1.05 at 21%")
		out, err := netsuite.NewPurchase(inv, purchaseOptions(t))
		require.NoError(t, err)
		lines := expenseLines(t, out)
		assert.Equal(t, []any{0.08, 0.07, 0.07}, []any{lines[0]["tax1Amt"], lines[1]["tax1Amt"], lines[2]["tax1Amt"]})
	})

	t.Run("mixed rates", func(t *testing.T) {
		inv := purchase(t, func(doc map[string]any) {
			doc["lines"] = append(doc["lines"].([]any), map[string]any{"quantity": "2", "item": map[string]any{"name": "Water", "price": "1.19"}, "taxes": []any{map[string]any{"cat": "VAT", "rate": "reduced"}}})
		})
		out, err := netsuite.NewPurchase(inv, purchaseOptions(t))
		require.NoError(t, err)
		lines := expenseLines(t, out)
		require.Len(t, lines, 5)
		assert.Equal(t, map[string]any{"id": "7"}, lines[3]["taxCode"], "R-ES")
		assert.Equal(t, 0.24, lines[3]["tax1Amt"])
	})

	t.Run("pending approval", func(t *testing.T) {
		o := purchaseOptions(t)
		o.PendingApproval = true
		out, err := netsuite.NewPurchase(purchase(t, nil), o)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"id": "1"}, out.Body["approvalStatus"])
	})

	t.Run("credit note", func(t *testing.T) {
		inv := purchase(t, func(doc map[string]any) {
			doc["type"] = "credit-note"
			doc["preceding"] = []any{map[string]any{"series": "F2026", "code": "1001", "issue_date": "2026-09-01"}}
			delete(doc, "payment")
		})
		out, err := netsuite.NewPurchase(inv, purchaseOptions(t))
		require.NoError(t, err)
		assert.Equal(t, netsuite.RecordTypeVendorCredit, out.RecordType)
		assert.NotContains(t, out.Body, "dueDate")
		require.Len(t, out.Notices, 1)
		assert.Equal(t, "preceding", out.Notices[0].Path)
	})

	t.Run("retained taxes", func(t *testing.T) {
		inv := purchase(t, func(doc map[string]any) {
			doc["lines"] = []any{map[string]any{"quantity": "1", "item": map[string]any{"name": "Consulting", "price": "100.00"}, "taxes": []any{
				map[string]any{"cat": "VAT", "rate": "general"},
				map[string]any{"cat": "IRPF", "percent": "15%"},
			}}}
			delete(doc, "discounts")
		})
		_, err := netsuite.NewPurchase(inv, purchaseOptions(t))
		assert.ErrorContains(t, err, "retained taxes")
	})

	t.Run("missing options", func(t *testing.T) {
		_, err := netsuite.NewPurchase(purchase(t, nil), &netsuite.PurchaseOptions{Subsidiary: "3"})
		assert.ErrorContains(t, err, "are required")
	})
}

func TestCheckRecordTotals(t *testing.T) {
	inv := purchase(t, nil)
	assert.NoError(t, netsuite.CheckRecordTotals(inv, json.RawMessage(`{"total": 187.95, "taxTotal": 32.62}`)))
	assert.NoError(t, netsuite.CheckRecordTotals(inv, json.RawMessage(`{"userTotal": 187.96, "taxTotal": 32.63}`)), "a subunit for the one rate")
	err := netsuite.CheckRecordTotals(inv, json.RawMessage(`{"total": 188.95, "taxTotal": 32.62}`))
	assert.ErrorContains(t, err, "total is 188.95 in NetSuite, but 187.95 in the invoice")
}
