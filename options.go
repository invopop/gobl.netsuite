package netsuite

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/invopop/gobl/currency"
	"github.com/invopop/gobl/l10n"
	"github.com/invopop/gobl/org"
)

// Option configures a conversion.
type Option func(*options)

type options struct {
	presets    []string
	presetsSet bool
	mappings   []*Mapping
	timeout    time.Duration
	taxEngine  TaxEngine
	supplier   *org.Party
	inboxes    []*org.Inbox
	base       currency.Code
}

// WithSupplier sets the supplier of transactions without a subsidiary, as
// in accounts without OneWorld, which also need WithBaseCurrency. A
// transaction's subsidiary always provides the supplier when it has one.
// The party is copied, so rules applied to the supplier don't change it.
func WithSupplier(p *org.Party) Option {
	return func(o *options) {
		o.supplier = p
	}
}

// WithInboxes adds inboxes to the supplier, such as those a network like
// Peppol registered it with, which NetSuite doesn't keep. They're copied,
// and those the supplier already has are skipped.
func WithInboxes(inboxes ...*org.Inbox) Option {
	return func(o *options) {
		o.inboxes = append(o.inboxes, inboxes...)
	}
}

// WithBaseCurrency sets the currency the supplier keeps its books in, which
// exchange rates convert into, for transactions without a subsidiary.
func WithBaseCurrency(c currency.Code) Option {
	return func(o *options) {
		o.base = c
	}
}

// TaxEngine is how a NetSuite account calculates tax.
type TaxEngine string

// Tax engines.
const (
	// TaxEngineAuto uses the transaction's tax details when it has any,
	// and the tax codes of its lines otherwise.
	TaxEngineAuto TaxEngine = ""
	// TaxEngineLegacy is NetSuite's original tax, with a tax code and rate
	// on each line.
	TaxEngineLegacy TaxEngine = "legacy"
	// TaxEngineSuiteTax is NetSuite's SuiteTax, where the taxes of each line
	// are in the transaction's tax details.
	TaxEngineSuiteTax TaxEngine = "suitetax"
)

// WithTaxEngine sets how the account calculates tax, instead of detecting
// it from the transaction.
func WithTaxEngine(e TaxEngine) Option {
	return func(o *options) {
		o.taxEngine = e
	}
}

// WithPresets sets the presets to use, in order, instead of the defaults:
// the "default" preset followed by the one for the supplier's country, if
// any. With no names, no presets are used, so tax codes are only converted
// using the mappings provided.
func WithPresets(names ...string) Option {
	return func(o *options) {
		o.presets = names
		o.presetsSet = true
	}
}

// WithMapping adds a mapping, such as an account's own, merged after the
// presets and any previous mappings so that it can override them.
func WithMapping(m *Mapping) Option {
	return func(o *options) {
		o.mappings = append(o.mappings, m)
	}
}

// WithRuleTimeout sets how long a rule may run on a single element.
func WithRuleTimeout(d time.Duration) Option {
	return func(o *options) {
		o.timeout = d
	}
}

func newOptions(opts []Option) *options {
	o := &options{timeout: defaultRuleTimeout}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// PresetDefault is the preset used for all countries, which converts tax
// codes according to their properties.
const PresetDefault = "default"

// BuildMapping provides the effective mapping that a conversion with the
// options would use for a supplier in the country, after merging the presets
// and mappings. Useful to show how tax codes will be converted.
func BuildMapping(country l10n.TaxCountryCode, opts ...Option) (*Mapping, error) {
	return newOptions(opts).mapping(country)
}

// mapping builds the effective mapping from the presets and mappings. By
// default the default preset is used, followed by the one named after the
// supplier's country, if any.
func (o *options) mapping(country l10n.TaxCountryCode) (*Mapping, error) {
	names := o.presets
	if !o.presetsSet {
		names = []string{PresetDefault}
		if name := strings.ToLower(country.String()); slices.Contains(Presets(), name) {
			names = append(names, name)
		}
	}
	ms := make([]*Mapping, 0, len(names)+len(o.mappings))
	for _, name := range names {
		p, err := Preset(name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, p)
	}
	ms = append(ms, o.mappings...)
	m := Merge(ms...)
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("mapping: %w", err)
	}
	return m, nil
}

//go:embed mappings/*.json
var presetFiles embed.FS

// ErrUnknownPreset is returned when a preset does not exist.
var ErrUnknownPreset = errors.New("unknown preset")

// Presets lists the names of the available presets, such as "es".
func Presets() []string {
	files, _ := fs.Glob(presetFiles, "mappings/*.json")
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = strings.TrimSuffix(path.Base(f), ".json")
	}
	return names
}

// Preset returns the named preset mapping.
func Preset(name string) (*Mapping, error) {
	data, err := presetFiles.ReadFile("mappings/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPreset, name)
	}
	m := new(Mapping)
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("preset %q: %w", name, err)
	}
	return m, nil
}
