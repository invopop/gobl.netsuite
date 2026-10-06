package netsuite_test

import (
	"testing"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl/bill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromCreditMemo(t *testing.T) {
	t.Run("created from invoice", func(t *testing.T) {
		res, err := netsuite.FromCreditMemo(loadBundle(t, "examples/netsuite/creditmemo_created_from.json"))
		require.NoError(t, err)
		inv := res.Invoice
		assert.Equal(t, bill.InvoiceTypeCreditNote, inv.Type)
		assert.Equal(t, "creditmemo", inv.Meta[netsuite.MetaKeyNetSuiteType])
		// Created from and applied to the same invoice, referred to once.
		require.Len(t, inv.Preceding, 1)
		assert.Equal(t, "1", inv.Preceding[0].Code.String())
		assert.Equal(t, "2026-09-24", inv.Preceding[0].IssueDate.String())
		assert.Empty(t, res.Unmapped)
	})

	t.Run("applied to invoice", func(t *testing.T) {
		res, err := netsuite.FromCreditMemo(loadBundle(t, "examples/netsuite/creditmemo_applied.json"))
		require.NoError(t, err)
		require.Len(t, res.Invoice.Preceding, 1)
		assert.Equal(t, "4", res.Invoice.Preceding[0].Code.String())
	})

	t.Run("without related invoices", func(t *testing.T) {
		b := loadBundle(t, "examples/netsuite/creditmemo_applied.json")
		b.Related = nil
		res, err := netsuite.FromCreditMemo(b)
		require.NoError(t, err)
		assert.Empty(t, res.Invoice.Preceding)
		require.Len(t, res.Unmapped, 1)
		assert.Equal(t, "credit memo not created from or applied to an invoice", res.Unmapped[0].Message)
	})

	t.Run("rejects invoices", func(t *testing.T) {
		_, err := netsuite.FromCreditMemo(basicBundle(t))
		assert.ErrorContains(t, err, `expected type "custcred", got "custinvc"`)
	})
}

func TestConvert(t *testing.T) {
	res, err := netsuite.Convert(basicBundle(t))
	require.NoError(t, err)
	assert.Equal(t, bill.InvoiceTypeStandard, res.Invoice.Type)

	res, err = netsuite.Convert(loadBundle(t, "examples/netsuite/creditmemo_applied.json"))
	require.NoError(t, err)
	assert.Equal(t, bill.InvoiceTypeCreditNote, res.Invoice.Type)

	b := basicBundle(t)
	modify(t, &b.Transaction, func(m map[string]any) {
		m["type"] = map[string]any{"id": "salesord"}
	})
	_, err = netsuite.Convert(b)
	assert.EqualError(t, err, `unsupported transaction type "salesord"`)
}
