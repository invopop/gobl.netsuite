package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// probeOpts groups commands used to explore the data available in a
// NetSuite account, to help design and test the conversion to GOBL.
type probeOpts struct {
	*rootOpts
	outDir string
	limit  int
	txType string
}

func probe(o *rootOpts) *probeOpts {
	return &probeOpts{rootOpts: o}
}

func (p *probeOpts) cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Explore data in a NetSuite account using the REST web services",
		Long: "Explore data in a NetSuite account using the REST web services. Credentials are read from\n" +
			"the NETSUITE_ACCOUNT_ID, NETSUITE_CONSUMER_KEY, NETSUITE_CONSUMER_SECRET, NETSUITE_TOKEN_ID\n" +
			"and NETSUITE_TOKEN_SECRET environment variables, or a .env file.",
	}

	invoice := &cobra.Command{
		Use:   "invoice <id>",
		Short: "Fetch an invoice with its customer and subsidiary",
		Args:  cobra.ExactArgs(1),
		RunE:  p.runInvoice,
	}
	invoice.Flags().StringVarP(&p.outDir, "out", "o", "", "directory to save each record in, instead of printing to stdout")
	cmd.AddCommand(invoice)

	invoices := &cobra.Command{
		Use:   "invoices",
		Short: "List the most recently modified invoices",
		Args:  cobra.NoArgs,
		RunE:  p.runInvoices,
	}
	invoices.Flags().IntVarP(&p.limit, "limit", "l", 10, "maximum number of results")
	invoices.Flags().StringVarP(&p.txType, "type", "t", "CustInvc", "transaction type, e.g. CustInvc or CustCred")
	cmd.AddCommand(invoices)

	query := &cobra.Command{
		Use:   "query <suiteql>",
		Short: "Run a SuiteQL query",
		Args:  cobra.ExactArgs(1),
		RunE:  p.runQuery,
	}
	query.Flags().IntVarP(&p.limit, "limit", "l", 100, "maximum number of results")
	cmd.AddCommand(query)

	get := &cobra.Command{
		Use:   "get <path>",
		Short: "GET any path under /services/rest, e.g. record/v1/metadata-catalog/invoice",
		Args:  cobra.ExactArgs(1),
		RunE:  p.runGet,
	}
	cmd.AddCommand(get)

	return cmd
}

// recordRef is the reference NetSuite uses for related records.
type recordRef struct {
	ID      string `json:"id"`
	RefName string `json:"refName"`
}

func (p *probeOpts) runInvoice(cmd *cobra.Command, args []string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	id := args[0]

	var inv json.RawMessage
	if err := nc.GetRecord(ctx, "invoice", id, true, &inv); err != nil {
		return fmt.Errorf("fetching invoice %s: %w", id, err)
	}
	out := map[string]json.RawMessage{"invoice": inv}

	refs := struct {
		Entity     *recordRef `json:"entity"`
		Subsidiary *recordRef `json:"subsidiary"`
	}{}
	if err := json.Unmarshal(inv, &refs); err != nil {
		return fmt.Errorf("parsing invoice: %w", err)
	}

	// Related records are fetched on a best effort basis, as the role may
	// not have access to them, or the account may not use subsidiaries.
	related := []struct {
		key, recordType string
		ref             *recordRef
	}{
		{"customer", "customer", refs.Entity},
		{"subsidiary", "subsidiary", refs.Subsidiary},
	}
	for _, r := range related {
		if r.ref == nil || r.ref.ID == "" {
			continue
		}
		var data json.RawMessage
		if err := nc.GetRecord(ctx, r.recordType, r.ref.ID, true, &data); err != nil {
			cmd.PrintErrf("warning: fetching %s %s: %v\n", r.recordType, r.ref.ID, err)
			continue
		}
		out[r.key] = data
	}

	if p.outDir == "" {
		return writeJSON(cmd, out)
	}
	if err := os.MkdirAll(p.outDir, 0o755); err != nil {
		return err
	}
	for k, v := range out {
		fn := filepath.Join(p.outDir, fmt.Sprintf("invoice_%s_%s.json", id, k))
		if err := saveJSON(fn, v); err != nil {
			return err
		}
		cmd.PrintErrf("saved %s\n", fn)
	}
	return nil
}

func (p *probeOpts) runInvoices(cmd *cobra.Command, _ []string) error {
	sql := fmt.Sprintf(`SELECT id, tranid, trandate, BUILTIN.DF(entity) AS customer,
		BUILTIN.DF(subsidiary) AS subsidiary, BUILTIN.DF(currency) AS currency,
		foreigntotal, BUILTIN.DF(status) AS status, lastmodifieddate
		FROM transaction WHERE type = '%s' ORDER BY lastmodifieddate DESC`,
		strings.ReplaceAll(p.txType, "'", "''"))
	return p.query(cmd, sql)
}

func (p *probeOpts) runQuery(cmd *cobra.Command, args []string) error {
	return p.query(cmd, args[0])
}

func (p *probeOpts) query(cmd *cobra.Command, sql string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	res, err := nc.Query(cmd.Context(), sql, p.limit, 0)
	if err != nil {
		return err
	}
	return writeJSON(cmd, res)
}

func (p *probeOpts) runGet(cmd *cobra.Command, args []string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimPrefix(args[0], "/"))
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	var data json.RawMessage
	if err := nc.Get(cmd.Context(), "/services/rest/"+u.Path, u.Query(), &data); err != nil {
		return err
	}
	return writeJSON(cmd, data)
}

func writeJSON(cmd *cobra.Command, data any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func saveJSON(fn string, data json.RawMessage) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fn, append(out, '\n'), 0o644)
}
