package netsuite

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/invopop/gobl/org"
)

// RecordTypeVendor is the record type of suppliers.
const RecordTypeVendor = "vendor"

// VendorOptions provides the NetSuite records a new vendor refers to.
type VendorOptions struct {
	// Subsidiary is the internal ID of the subsidiary the vendor supplies.
	Subsidiary string
	// Currency is the internal ID of the vendor's currency.
	Currency string
	// ExternalID identifies the vendor, such as by its tax ID, so it's only
	// created once.
	ExternalID string
	// Fields are added to the record, such as custom fields.
	Fields map[string]any
	// Mapping provides vendor send rules to apply, with $src the party.
	Mapping *Mapping
}

// NewVendor prepares a vendor from a GOBL party, such as an invoice's
// supplier: its name, tax ID, first email and telephone, and first address
// as the default billing address. Payment details are never included, as
// they need checking before they are used.
func NewVendor(p *org.Party, o *VendorOptions) (*Outbound, error) {
	if p == nil || p.Name == "" {
		return nil, fmt.Errorf("the party must have a name")
	}
	if o == nil || o.Subsidiary == "" {
		return nil, fmt.Errorf("the subsidiary is required")
	}
	out := &Outbound{RecordType: RecordTypeVendor}
	d := vendorDetailsOf(p)
	body := map[string]any{
		"isPerson":    false,
		"companyName": truncate(d.name, 83),
		"legalName":   truncate(d.name, 83),
		"subsidiary":  ref(o.Subsidiary),
	}
	if d.taxID != "" {
		body["vatRegNumber"] = d.taxID
	}
	if d.email != "" {
		body["email"] = d.email
	}
	if d.phone != "" {
		body["phone"] = d.phone
	}
	if o.Currency != "" {
		body["currency"] = ref(o.Currency)
	}
	if o.ExternalID != "" {
		body["externalId"] = o.ExternalID
	}
	if a := d.address; a != nil {
		addr := map[string]any{
			"addressee": truncate(d.name, 83),
			"addr1":     a.Addr1,
			"city":      a.City,
			"zip":       a.Zip,
		}
		if a.Addr2 != "" {
			addr["addr2"] = a.Addr2
		}
		if a.State != "" {
			addr["state"] = a.State
		}
		if a.Country.ID != "" {
			addr["country"] = ref(a.Country.ID)
		}
		body["addressBook"] = map[string]any{"items": []any{
			map[string]any{"defaultBilling": true, "defaultShipping": false, "addressBookAddress": addr},
		}}
	}
	for k, v := range o.Fields {
		body[k] = v
	}
	if len(p.Identities) > 0 {
		out.notice("identities", "identities other than the tax ID are not recorded")
	}
	if err := applySendRules(o.Mapping, &body, p, []*sendElement{{scope: ScopeVendor, target: &body, src: p}}); err != nil {
		return nil, err
	}
	out.Body = body
	return out, nil
}

// VendorChange is a difference between a party and the vendor it is linked
// to.
type VendorChange struct {
	// Field is the vendor's field, such as "email" or "address".
	Field string `json:"field"`
	// Current is the vendor's value, and Proposed the party's.
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
}

// String describes the change for a reviewer.
func (c *VendorChange) String() string {
	if c.Current == "" {
		return fmt.Sprintf("%s: set to %q", c.Field, c.Proposed)
	}
	return fmt.Sprintf("%s: %q to %q", c.Field, c.Current, c.Proposed)
}

// CompareVendor lists the details of a party that differ from the vendor's,
// as read with its subresources expanded. Details the party doesn't have
// are not compared, so a document without an email doesn't propose removing
// the vendor's.
func CompareVendor(p *org.Party, record json.RawMessage) ([]*VendorChange, error) {
	rec := struct {
		CompanyName  string `json:"companyName"`
		VATRegNumber string `json:"vatRegNumber"`
		Email        string `json:"email"`
		Phone        string `json:"phone"`
		AddressBook  struct {
			Items []struct {
				DefaultBilling bool        `json:"defaultBilling"`
				Address        *addressRec `json:"addressBookAddress"`
			} `json:"items"`
		} `json:"addressBook"`
	}{}
	if err := json.Unmarshal(record, &rec); err != nil {
		return nil, fmt.Errorf("parsing vendor: %w", err)
	}
	d := vendorDetailsOf(p)
	var changes []*VendorChange
	compare := func(field, current, proposed string) {
		if proposed != "" && normalText(current) != normalText(proposed) {
			changes = append(changes, &VendorChange{Field: field, Current: current, Proposed: proposed})
		}
	}
	compare("name", rec.CompanyName, d.name)
	if d.taxID != "" && alphaNum(rec.VATRegNumber) != alphaNum(d.taxID) {
		changes = append(changes, &VendorChange{Field: "tax ID", Current: rec.VATRegNumber, Proposed: d.taxID})
	}
	compare("email", rec.Email, d.email)
	// NetSuite stores phone numbers without spaces or punctuation.
	if d.phone != "" && phoneDigits(rec.Phone) != phoneDigits(d.phone) {
		changes = append(changes, &VendorChange{Field: "phone", Current: rec.Phone, Proposed: d.phone})
	}
	if d.address != nil {
		var current *addressRec
		for _, item := range rec.AddressBook.Items {
			if item.Address != nil && (current == nil || item.DefaultBilling) {
				current = item.Address
			}
		}
		compare("address", current.String(), d.address.String())
	}
	return changes, nil
}

// vendorDetails are the details of a party recorded on vendors.
type vendorDetails struct {
	name, taxID, email, phone string
	address                   *addressRec
}

func vendorDetailsOf(p *org.Party) *vendorDetails {
	d := &vendorDetails{name: strings.TrimSpace(p.Name)}
	if p.TaxID != nil && p.TaxID.Code != "" {
		country := strings.ToUpper(p.TaxID.Country.String())
		d.taxID = country + strings.TrimPrefix(strings.ToUpper(p.TaxID.Code.String()), country)
	}
	if len(p.Emails) > 0 && p.Emails[0] != nil {
		d.email = p.Emails[0].Address
	}
	if len(p.Telephones) > 0 && p.Telephones[0] != nil {
		d.phone = p.Telephones[0].Number
	}
	if len(p.Addresses) > 0 && p.Addresses[0] != nil {
		a := p.Addresses[0]
		line := strings.TrimSpace(a.Street + " " + a.Number)
		if a.PostOfficeBox != "" && line == "" {
			line = "PO Box " + a.PostOfficeBox
		}
		rec := &addressRec{Addr1: line, Addr2: a.StreetExtra, City: a.Locality, Zip: a.Code.String(), State: firstOf(a.Region, a.State.String())}
		rec.Country.ID = a.Country.String()
		d.address = rec
	}
	return d
}

// addressRec is a NetSuite address, as read from an address book, or
// prepared from a GOBL address.
type addressRec struct {
	Addr1   string `json:"addr1"`
	Addr2   string `json:"addr2"`
	City    string `json:"city"`
	State   string `json:"state"`
	Zip     string `json:"zip"`
	Country struct {
		ID string `json:"id"`
	} `json:"country"`
}

// String formats the address on one line, for comparing and describing.
func (a *addressRec) String() string {
	if a == nil {
		return ""
	}
	var parts []string
	for _, p := range []string{a.Addr1, a.Addr2, strings.TrimSpace(a.Zip + " " + a.City), a.State, a.Country.ID} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// normalText compares text ignoring case and repeated spaces.
func normalText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// phoneDigits keeps a phone number's digits and leading plus.
func phoneDigits(s string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if strings.HasPrefix(strings.TrimSpace(s), "+") {
		return "+" + digits
	}
	return digits
}

// alphaNum keeps letters and digits, upper case.
func alphaNum(s string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			return r
		}
		return -1
	}, s))
}
