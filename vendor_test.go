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

func orange() *org.Party {
	return &org.Party{
		Name:       "Orange Espagne, S.A.",
		TaxID:      &tax.Identity{Country: "ES", Code: "A82009812"},
		Emails:     []*org.Email{{Address: "facturas@orange.es"}},
		Telephones: []*org.Telephone{{Number: "+34 900 900 900"}},
		Addresses: []*org.Address{{
			Street: "Paseo del Club Deportivo", Number: "1", StreetExtra: "Parque Empresarial La Finca, Edificio 8",
			Locality: "Pozuelo de Alarcón", Region: "Madrid", Code: "28223", Country: "ES",
		}},
	}
}

// orangeVendor is the vendor record as NetSuite returns it, with
// subresources expanded.
const orangeVendor = `{
	"id": "8", "companyName": "Orange Espagne, S.A.", "vatRegNumber": "ESA82009812",
	"email": "facturas@orange.es", "phone": "+34900900900",
	"addressBook": {"items": [{"defaultBilling": true, "addressBookAddress": {
		"addr1": "Paseo del Club Deportivo 1", "addr2": "Parque Empresarial La Finca, Edificio 8",
		"city": "Pozuelo de Alarcón", "state": "Madrid", "zip": "28223", "country": {"id": "ES", "refName": "Spain"}}}]}
}`

func TestNewVendor(t *testing.T) {
	out, err := netsuite.NewVendor(orange(), &netsuite.VendorOptions{
		Subsidiary: "3", Currency: "1", ExternalID: "invopop-ESA82009812",
		Fields: map[string]any{"custentity_invopop_review": map[string]string{"id": "1"}},
	})
	require.NoError(t, err)
	assert.Equal(t, netsuite.RecordTypeVendor, out.RecordType)
	b := out.Body
	assert.Equal(t, false, b["isPerson"])
	assert.Equal(t, "Orange Espagne, S.A.", b["companyName"])
	assert.Equal(t, "ESA82009812", b["vatRegNumber"])
	assert.Equal(t, "facturas@orange.es", b["email"])
	assert.Equal(t, "invopop-ESA82009812", b["externalId"])
	assert.Equal(t, map[string]string{"id": "1"}, b["custentity_invopop_review"])

	data, err := json.Marshal(b["addressBook"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"items":[{"defaultBilling":true,"defaultShipping":false,"addressBookAddress":{
		"addressee":"Orange Espagne, S.A.","addr1":"Paseo del Club Deportivo 1","addr2":"Parque Empresarial La Finca, Edificio 8",
		"city":"Pozuelo de Alarcón","state":"Madrid","zip":"28223","country":{"id":"ES"}}}]}`, string(data))

	_, err = netsuite.NewVendor(&org.Party{}, &netsuite.VendorOptions{Subsidiary: "3"})
	assert.ErrorContains(t, err, "must have a name")
}

func TestCompareVendor(t *testing.T) {
	t.Run("unchanged", func(t *testing.T) {
		changes, err := netsuite.CompareVendor(orange(), json.RawMessage(orangeVendor))
		require.NoError(t, err)
		assert.Empty(t, changes)
	})

	t.Run("case, spaces and missing details are ignored", func(t *testing.T) {
		p := orange()
		p.Name = "ORANGE  Espagne, S.A."
		p.Emails = nil
		p.TaxID.Code = "a-82009812"
		changes, err := netsuite.CompareVendor(p, json.RawMessage(orangeVendor))
		require.NoError(t, err)
		assert.Empty(t, changes)
	})

	t.Run("changed", func(t *testing.T) {
		p := orange()
		p.Emails[0].Address = "billing@orange.es"
		p.Addresses[0].Code = "28224"
		changes, err := netsuite.CompareVendor(p, json.RawMessage(orangeVendor))
		require.NoError(t, err)
		require.Len(t, changes, 2)
		assert.Equal(t, `email: "facturas@orange.es" to "billing@orange.es"`, changes[0].String())
		assert.Equal(t, "address", changes[1].Field)
		assert.Contains(t, changes[1].Proposed, "28224 Pozuelo de Alarcón")
	})

	t.Run("new details", func(t *testing.T) {
		changes, err := netsuite.CompareVendor(orange(), json.RawMessage(`{"companyName":"Orange Espagne, S.A.","vatRegNumber":"ESA82009812"}`))
		require.NoError(t, err)
		require.Len(t, changes, 3)
		assert.Equal(t, `email: set to "facturas@orange.es"`, changes[0].String())
	})
}
