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

	"github.com/invopop/gobl/l10n"
)

// Option configures a conversion.
type Option func(*options)

type options struct {
	presets    []string
	presetsSet bool
	mappings   []*Mapping
	timeout    time.Duration
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
