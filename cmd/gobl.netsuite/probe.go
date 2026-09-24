package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	goblnetsuite "github.com/invopop/gobl.netsuite"
	"github.com/spf13/cobra"
)

// probeOpts groups commands used to explore the data available in a
// NetSuite account, to help design and test the conversion to GOBL.
type probeOpts struct {
	*rootOpts
	outFile    string
	convert    bool
	stripLinks bool
	limit      int
	txType     string
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
		Short: "Fetch an invoice and the related records needed to convert it, as a bundle",
		Args:  cobra.ExactArgs(1),
		RunE:  p.runInvoice,
	}
	invoice.Flags().StringVarP(&p.outFile, "out", "o", "", "file to write to, instead of stdout")
	invoice.Flags().BoolVar(&p.convert, "convert", false, "convert the bundle into a GOBL envelope")
	invoice.Flags().BoolVar(&p.stripLinks, "strip-links", false, "remove links, which contain the account ID, e.g. to create test fixtures")
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

	schema := &cobra.Command{
		Use:   "schema <record-type>",
		Short: "Fetch the account specific JSON Schema for a record type, e.g. invoice",
		Args:  cobra.ExactArgs(1),
		RunE:  p.runSchema,
	}
	cmd.AddCommand(schema)

	return cmd
}

func (p *probeOpts) runInvoice(cmd *cobra.Command, args []string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	b, err := goblnetsuite.FetchInvoice(cmd.Context(), nc, args[0])
	if err != nil {
		return err
	}
	if p.convert {
		return convertBundle(cmd, b, p.outFile)
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if p.stripLinks {
		if data, err = stripLinks(data); err != nil {
			return err
		}
	}
	return output(cmd, p.outFile, data)
}

func (p *probeOpts) runInvoices(cmd *cobra.Command, _ []string) error {
	sql := fmt.Sprintf(`SELECT id, tranid, trandate, BUILTIN.DF(entity) AS customer,
		BUILTIN.DF(currency) AS currency,
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

func (p *probeOpts) runSchema(cmd *cobra.Command, args []string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	var data json.RawMessage
	if err := nc.RecordSchema(cmd.Context(), args[0], &data); err != nil {
		return err
	}
	return writeJSON(cmd, data)
}

func writeJSON(cmd *cobra.Command, data any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// output writes indented JSON data to the file, or stdout if empty.
func output(cmd *cobra.Command, file string, data []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	if file == "" {
		_, err := cmd.OutOrStdout().Write(buf.Bytes())
		return err
	}
	if err := os.WriteFile(file, buf.Bytes(), 0o644); err != nil {
		return err
	}
	cmd.PrintErrf("saved %s\n", file)
	return nil
}

// stripLinks removes all "links" properties from JSON data.
func stripLinks(data []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	var strip func(v any)
	strip = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			delete(t, "links")
			for _, c := range t {
				strip(c)
			}
		case []any:
			for _, c := range t {
				strip(c)
			}
		}
	}
	strip(v)
	return json.Marshal(v)
}
