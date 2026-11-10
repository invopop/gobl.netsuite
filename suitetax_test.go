package netsuite_test

import (
	"encoding/json"
	"testing"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl/org"
	"github.com/invopop/gobl/tax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuiteTax(t *testing.T) {
	t.Run("detected from the tax details", func(t *testing.T) {
		res, err := netsuite.ImportInvoice(loadBundle(t, "examples/netsuite/invoice_suitetax.json"))
		require.NoError(t, err)
		require.Len(t, res.Invoice.Lines, 1)
		require.Len(t, res.Invoice.Lines[0].Taxes, 1)
		assert.Equal(t, "21%", res.Invoice.Lines[0].Taxes[0].Percent.String())
		assert.Empty(t, res.Unmapped)
	})

	t.Run("forced legacy ignores the tax details", func(t *testing.T) {
		res, err := netsuite.ImportInvoice(loadBundle(t, "examples/netsuite/invoice_suitetax.json"),
			netsuite.WithTaxEngine(netsuite.TaxEngineLegacy))
		require.NoError(t, err)
		assert.Empty(t, res.Invoice.Lines[0].Taxes)
		require.NotEmpty(t, res.Unmapped)
		assert.Equal(t, "line has no tax code", res.Unmapped[0].Message)
	})

	t.Run("forced SuiteTax without tax details", func(t *testing.T) {
		_, err := netsuite.ImportInvoice(loadBundle(t, "examples/netsuite/invoice_basic.json"),
			netsuite.WithTaxEngine(netsuite.TaxEngineSuiteTax))
		assert.ErrorContains(t, err, "doesn't seem to use SuiteTax")
	})

	t.Run("more than one tax on a line", func(t *testing.T) {
		b := loadBundle(t, "examples/netsuite/invoice_suitetax.json")
		var tx map[string]any
		require.NoError(t, json.Unmarshal(b.Transaction, &tx))
		details := tx["taxDetails"].(map[string]any)
		items := details["items"].([]any)
		details["items"] = append(items, items[0])
		var err error
		b.Transaction, err = json.Marshal(tx)
		require.NoError(t, err)

		_, err = netsuite.ImportInvoice(b)
		assert.ErrorContains(t, err, "line has 2 taxes in its tax details")
	})

	t.Run("unknown engine", func(t *testing.T) {
		_, err := netsuite.ImportInvoice(loadBundle(t, "examples/netsuite/invoice_basic.json"),
			netsuite.WithTaxEngine("other"))
		assert.ErrorContains(t, err, `unknown tax engine "other"`)
	})
}

func TestSupplier(t *testing.T) {
	party := func() *org.Party {
		return &org.Party{
			Name:      "Invopop SL",
			TaxID:     &tax.Identity{Country: "ES", Code: "B85905495"},
			Inboxes:   []*org.Inbox{{Scheme: "0088", Code: "1234567890128"}},
			Addresses: []*org.Address{{Locality: "Madrid", Country: "ES"}},
		}
	}
	noSubsidiary := func(t *testing.T) *netsuite.Bundle {
		b := loadBundle(t, "examples/netsuite/invoice_basic.json")
		b.Subsidiary = nil
		return b
	}

	t.Run("without OneWorld", func(t *testing.T) {
		p := party()
		res, err := netsuite.ImportInvoice(noSubsidiary(t), netsuite.WithSupplier(p), netsuite.WithBaseCurrency("EUR"))
		require.NoError(t, err)
		sup := res.Invoice.Supplier
		assert.Equal(t, "Invopop SL", sup.Name)
		require.Len(t, sup.Inboxes, 1)
		assert.Equal(t, "ES", res.Invoice.GetRegime().String())
		assert.Empty(t, res.Invoice.ExchangeRates)
		assert.NotSame(t, p, sup, "the party given is copied")
	})

	t.Run("country from the address", func(t *testing.T) {
		p := party()
		p.TaxID = nil
		p.Addresses[0].Country = "PT"
		res, err := netsuite.ImportInvoice(noSubsidiary(t), netsuite.WithSupplier(p), netsuite.WithBaseCurrency("EUR"))
		require.NoError(t, err)
		assert.Equal(t, "PT", res.Invoice.GetRegime().String())
	})

	t.Run("exchange rate into the base currency", func(t *testing.T) {
		res, err := netsuite.ImportInvoice(noSubsidiary(t), netsuite.WithSupplier(party()), netsuite.WithBaseCurrency("USD"))
		require.NoError(t, err)
		require.Len(t, res.Invoice.ExchangeRates, 1)
		assert.Equal(t, "USD", res.Invoice.ExchangeRates[0].To.String())
	})

	t.Run("requires the base currency", func(t *testing.T) {
		_, err := netsuite.ImportInvoice(noSubsidiary(t), netsuite.WithSupplier(party()))
		assert.ErrorContains(t, err, "the base currency is required")
	})

	t.Run("requires a supplier", func(t *testing.T) {
		_, err := netsuite.ImportInvoice(noSubsidiary(t), netsuite.WithBaseCurrency("EUR"))
		assert.ErrorContains(t, err, "a subsidiary, or a supplier party")
	})

	t.Run("the subsidiary wins", func(t *testing.T) {
		p := party()
		p.Name = "Party with old details"
		res, err := netsuite.ImportInvoice(loadBundle(t, "examples/netsuite/invoice_basic.json"), netsuite.WithSupplier(p))
		require.NoError(t, err)
		assert.NotEqual(t, "Party with old details", res.Invoice.Supplier.Name)
		assert.Empty(t, res.Invoice.Supplier.Inboxes)
	})

	t.Run("adds registered inboxes", func(t *testing.T) {
		in := &org.Inbox{Key: "peppol", Scheme: "0088", Code: "1234567890128"}
		res, err := netsuite.ImportInvoice(loadBundle(t, "examples/netsuite/invoice_basic.json"), netsuite.WithInboxes(in, nil))
		require.NoError(t, err)
		sup := res.Invoice.Supplier
		require.Len(t, sup.Inboxes, 1)
		assert.Equal(t, "1234567890128", sup.Inboxes[0].Code.String())
		assert.NotSame(t, in, sup.Inboxes[0], "copied")

		p := party()
		res, err = netsuite.ImportInvoice(noSubsidiary(t), netsuite.WithSupplier(p), netsuite.WithBaseCurrency("EUR"),
			netsuite.WithInboxes(&org.Inbox{Scheme: "0088", Code: "1234567890128"}))
		require.NoError(t, err)
		assert.Len(t, res.Invoice.Supplier.Inboxes, 1, "already there")
	})
}
