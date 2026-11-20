package netsuite

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/invopop/gobl/cbc"
	"github.com/invopop/gobl/l10n"
	"github.com/invopop/gobl/org"
	"github.com/invopop/gobl/tax"
)

// newSupplier builds the supplier from the invoice's subsidiary.
func newSupplier(sub *Subsidiary) *org.Party {
	p := &org.Party{
		Name: firstOf(sub.LegalName, sub.Name),
	}
	if sub.FederalIDNumber != "" {
		p.TaxID = &tax.Identity{
			Country: l10n.TaxCountryCode(sub.Country.ID),
			Code:    cbc.Code(sub.FederalIDNumber),
		}
	}
	if a := newAddress(sub.MainAddress); a != nil {
		p.Addresses = []*org.Address{a}
	}
	if sub.Email != "" {
		p.Emails = []*org.Email{{Address: sub.Email}}
	}
	return p
}

// copyParty copies a party given by the caller, so the conversion can
// change it.
func copyParty(p *org.Party) (*org.Party, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	out := new(org.Party)
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// addInboxes adds copies of the inboxes the party doesn't have yet.
func addInboxes(p *org.Party, inboxes []*org.Inbox) error {
	for _, in := range inboxes {
		if in == nil || slices.ContainsFunc(p.Inboxes, func(have *org.Inbox) bool { return sameInbox(have, in) }) {
			continue
		}
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		cp := new(org.Inbox)
		if err := json.Unmarshal(data, cp); err != nil {
			return err
		}
		p.Inboxes = append(p.Inboxes, cp)
	}
	return nil
}

func sameInbox(a, b *org.Inbox) bool {
	return a != nil && a.Scheme == b.Scheme && a.Code == b.Code && a.URL == b.URL && a.Email == b.Email
}

// newCustomer builds the customer, preferring details on the invoice itself
// as they may override those of the customer record.
func newCustomer(inv *Transaction, cus *Customer) *org.Party {
	p := new(org.Party)
	if cus != nil {
		if cus.IsPerson {
			p.Name = strings.TrimSpace(cus.FirstName + " " + cus.LastName)
		} else {
			p.Name = cus.CompanyName
		}
	}
	if p.Name == "" && inv.BillingAddress != nil {
		p.Name = inv.BillingAddress.Addressee
	}
	if p.Name == "" && inv.Entity != nil {
		p.Name = inv.Entity.RefName
	}

	addr := newAddress(inv.BillingAddress)
	if addr != nil {
		p.Addresses = []*org.Address{addr}
	}

	code := inv.VATRegNum
	if code == "" && cus != nil {
		code = cus.VATRegNumber
	}
	if code != "" {
		// GOBL removes the country prefix from the code if present.
		p.TaxID = &tax.Identity{Code: cbc.Code(code)}
		if addr != nil {
			p.TaxID.Country = l10n.TaxCountryCode(addr.Country)
		}
	}

	email := inv.Email
	if email == "" && cus != nil {
		email = cus.Email
	}
	if email != "" {
		p.Emails = []*org.Email{{Address: email}}
	}
	if cus != nil && cus.Phone != "" {
		p.Telephones = []*org.Telephone{{Number: cus.Phone}}
	}
	return p
}

func newAddress(a *Address) *org.Address {
	if a == nil {
		return nil
	}
	addr := &org.Address{
		Street:      a.Addr1,
		StreetExtra: strings.TrimSpace(a.Addr2 + " " + a.Addr3),
		Locality:    a.City,
		Region:      a.State,
		Code:        cbc.Code(a.Zip),
	}
	if a.Country != nil {
		addr.Country = l10n.ISOCountryCode(a.Country.ID)
	}
	if addr.Street == "" && addr.StreetExtra == "" && addr.Locality == "" &&
		addr.Region == "" && addr.Code == "" && addr.Country == "" {
		return nil
	}
	return addr
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
