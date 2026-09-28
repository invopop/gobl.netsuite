package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/invopop/gobl"
	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/spf13/cobra"
)

type convertOpts struct {
	*rootOpts
	outFile string
}

func convert(o *rootOpts) *convertOpts {
	return &convertOpts{rootOpts: o}
}

func (c *convertOpts) cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "convert [infile]",
		Short: "Convert a NetSuite invoice or credit memo bundle into a GOBL envelope",
		Long: "Convert a NetSuite bundle, as produced by `probe invoice` or `probe creditmemo`, into a GOBL\n" +
			"envelope. " +
			"Reads from stdin if no file is given.",
		Args: cobra.MaximumNArgs(1),
		RunE: c.runE,
	}
	cmd.Flags().StringVarP(&c.outFile, "out", "o", "", "file to write to, instead of stdout")
	return cmd
}

func (c *convertOpts) runE(cmd *cobra.Command, args []string) error {
	in := cmd.InOrStdin()
	if len(args) > 0 && args[0] != "-" {
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck
		in = f
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("reading input: %w", err)
	}
	b := new(netsuite.Bundle)
	if err := json.Unmarshal(data, b); err != nil {
		return fmt.Errorf("parsing bundle: %w", err)
	}
	return convertBundle(cmd, b, c.outFile)
}

// convertBundle converts, calculates and outputs the GOBL envelope. Notices
// about unmapped data are printed to stderr, and an error is returned after
// the output if the invoice is invalid or its totals differ from NetSuite's.
func convertBundle(cmd *cobra.Command, b *netsuite.Bundle, outFile string) error {
	res, err := netsuite.Convert(b)
	if err != nil {
		return err
	}
	env, err := gobl.Envelop(res.Invoice)
	if err != nil {
		return err
	}
	if err := env.Calculate(); err != nil {
		return fmt.Errorf("calculating: %w", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := output(cmd, outFile, data); err != nil {
		return err
	}

	for _, n := range res.Unmapped {
		cmd.PrintErrf("unmapped: %s: %s\n", n.Path, n.Message)
	}
	var errs []error
	if err := env.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("validation: %w", err))
	}
	if err := res.CheckTotals(); err != nil {
		errs = append(errs, err)
	}
	for _, n := range res.Warnings {
		cmd.PrintErrf("warning: %s: %s\n", n.Path, n.Message)
	}
	return errors.Join(errs...)
}
