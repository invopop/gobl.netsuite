package main

import (
	"encoding/json"
	"fmt"
	"os"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/spf13/cobra"
)

// mappingFlags are the flags that configure the mapping used to convert.
type mappingFlags struct {
	files     []string
	presets   []string
	noPresets bool
}

func (f *mappingFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringArrayVarP(&f.files, "mapping", "m", nil, "mapping file to apply after the presets, may be repeated")
	cmd.Flags().StringSliceVar(&f.presets, "presets", nil, "presets to use instead of the default for the supplier's country")
	cmd.Flags().BoolVar(&f.noPresets, "no-presets", false, "do not use any presets")
}

func (f *mappingFlags) options() ([]netsuite.Option, error) {
	var opts []netsuite.Option
	switch {
	case f.noPresets:
		opts = append(opts, netsuite.WithPresets())
	case len(f.presets) > 0:
		opts = append(opts, netsuite.WithPresets(f.presets...))
	}
	for _, file := range f.files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		m := new(netsuite.Mapping)
		if err := json.Unmarshal(data, m); err != nil {
			return nil, fmt.Errorf("parsing mapping %s: %w", file, err)
		}
		opts = append(opts, netsuite.WithMapping(m))
	}
	return opts, nil
}

func presetsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "presets [name]",
		Short: "List the available mapping presets, or show one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				for _, name := range netsuite.Presets() {
					cmd.Println(name)
				}
				return nil
			}
			m, err := netsuite.Preset(args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd, m)
		},
	}
}
