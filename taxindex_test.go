package netsuite_test

import (
	"encoding/json"
	"os"
	"testing"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl/cbc"
	"github.com/invopop/gobl/num"
	"github.com/invopop/gobl/tax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTaxCodes indexes the tax codes of a Spanish subsidiary, with the
// default and Spanish presets.
func testTaxCodes(t *testing.T) *netsuite.TaxCodeIndex {
	t.Helper()
	data, err := os.ReadFile("examples/taxcodes/es.json")
	require.NoError(t, err)
	var recs []json.RawMessage
	require.NoError(t, json.Unmarshal(data, &recs))
	m, err := netsuite.BuildMapping("ES")
	require.NoError(t, err)
	x, err := netsuite.NewTaxCodeIndex(m, recs)
	require.NoError(t, err)
	return x
}

func combo(key cbc.Key, percent string) *tax.Combo {
	c := &tax.Combo{Category: tax.CategoryVAT, Key: key}
	if percent != "" {
		p, err := num.PercentageFromString(percent)
		if err != nil {
			panic(err)
		}
		c.Percent = &p
	}
	return c
}

func TestTaxCodeIndex(t *testing.T) {
	x := testTaxCodes(t)
	tests := []struct {
		name string
		c    *tax.Combo
		dir  netsuite.Direction
		code string
	}{
		{"standard", combo(tax.KeyStandard, "21%"), netsuite.DirectionPurchase, "S-ES"},
		{"no key is standard", combo("", "21%"), netsuite.DirectionSale, "S-ES"},
		{"reduced", combo(tax.KeyStandard, "10%"), netsuite.DirectionPurchase, "R-ES"},
		{"super-reduced", combo(tax.KeyStandard, "4%"), netsuite.DirectionSale, "R2-ES"},
		{"zero, ignoring a 0% percent", combo(tax.KeyZero, "0%"), netsuite.DirectionPurchase, "Z-ES"},
		{"exempt", combo(tax.KeyExempt, ""), netsuite.DirectionPurchase, "EX-ES"},
		{"intra-community sale", combo(tax.KeyIntraCommunity, ""), netsuite.DirectionSale, "ESSS-ES"},
		{"intra-community purchase as reverse charge, preferring purchase codes", combo(tax.KeyIntraCommunity, ""), netsuite.DirectionPurchase, "ESSP-ES"},
		{"reverse charge sale", combo(tax.KeyReverseCharge, ""), netsuite.DirectionSale, "RC-ES"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, err := x.Find(tt.c, "ES", tt.dir)
			require.NoError(t, err)
			assert.Equal(t, tt.code, code.ItemID)
		})
	}

	t.Run("no match", func(t *testing.T) {
		_, err := x.Find(combo(tax.KeyStandard, "7%"), "ES", netsuite.DirectionPurchase)
		assert.ErrorContains(t, err, "no purchase tax code for VAT standard 7% in ES")
	})

	t.Run("another country", func(t *testing.T) {
		_, err := x.Find(combo(tax.KeyStandard, "21%"), "PT", netsuite.DirectionPurchase)
		assert.ErrorContains(t, err, "in PT")
	})
}
