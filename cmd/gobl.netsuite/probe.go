package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/spf13/cobra"
)

// probeOpts groups commands used to explore the data available in a
// NetSuite account, to help design and test the conversion to GOBL.
type probeOpts struct {
	*rootOpts
	mappingFlags
	outFile    string
	convert    bool
	stripLinks bool
	replace    []string
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

	for _, rt := range []string{netsuite.RecordTypeInvoice, netsuite.RecordTypeCreditMemo} {
		tx := &cobra.Command{
			Use:   rt + " <id>",
			Short: "Fetch a " + rt + " and the related records needed to convert it, as a bundle",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return p.runTransaction(cmd, rt, args[0])
			},
		}
		tx.Flags().StringVarP(&p.outFile, "out", "o", "", "file to write to, instead of stdout")
		tx.Flags().BoolVar(&p.convert, "convert", false, "convert the bundle into a GOBL envelope")
		tx.Flags().BoolVar(&p.stripLinks, "strip-links", false, "remove links, which contain the account ID, e.g. to create test fixtures")
		p.add(tx)
		cmd.AddCommand(tx)
	}

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

	create := &cobra.Command{
		Use:   "create <record-type> [file]",
		Short: "Create a record from JSON, e.g. to add test data, printing the new internal ID",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  p.runCreate,
	}
	cmd.AddCommand(create)

	transform := &cobra.Command{
		Use:   "transform <record-type> <id> <target-type> [file]",
		Short: "Create a record from another, e.g. a credit memo from an invoice, printing the new internal ID",
		Args:  cobra.RangeArgs(3, 4),
		RunE:  p.runTransform,
	}
	cmd.AddCommand(transform)

	update := &cobra.Command{
		Use:   "update <record-type> <id> [file]",
		Short: "Update a record's fields from JSON, e.g. to adjust test data",
		Args:  cobra.RangeArgs(2, 3),
		RunE:  p.runUpdate,
	}
	update.Flags().StringSliceVar(&p.replace, "replace", nil, "sublists to replace entirely, e.g. item")
	cmd.AddCommand(update)

	return cmd
}

func (p *probeOpts) runTransaction(cmd *cobra.Command, recordType, id string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	fetch := netsuite.FetchInvoice
	if recordType == netsuite.RecordTypeCreditMemo {
		fetch = netsuite.FetchCreditMemo
	}
	b, err := fetch(cmd.Context(), nc, id)
	if err != nil {
		return err
	}
	if p.convert {
		opts, err := p.options()
		if err != nil {
			return err
		}
		return convertBundle(cmd, b, p.outFile, opts...)
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

func (p *probeOpts) runCreate(cmd *cobra.Command, args []string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	body, err := readBody(cmd, args[1:], true)
	if err != nil {
		return err
	}
	id, err := nc.CreateRecord(cmd.Context(), args[0], body)
	if err != nil {
		return err
	}
	cmd.Println(id)
	return nil
}

func (p *probeOpts) runTransform(cmd *cobra.Command, args []string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	body, err := readBody(cmd, args[3:], false)
	if err != nil {
		return err
	}
	id, err := nc.TransformRecord(cmd.Context(), args[0], args[1], args[2], body)
	if err != nil {
		return err
	}
	cmd.Println(id)
	return nil
}

func (p *probeOpts) runUpdate(cmd *cobra.Command, args []string) error {
	nc, err := p.netsuiteClient()
	if err != nil {
		return err
	}
	body, err := readBody(cmd, args[2:], true)
	if err != nil {
		return err
	}
	return nc.UpdateRecord(cmd.Context(), args[0], args[1], body, p.replace...)
}

// readBody reads a JSON body from the file in args, or stdin when "-" or,
// if stdin is the default, when no file is given.
func readBody(cmd *cobra.Command, args []string, stdin bool) (json.RawMessage, error) {
	in := cmd.InOrStdin()
	switch {
	case len(args) > 0 && args[0] != "-":
		f, err := os.Open(args[0])
		if err != nil {
			return nil, err
		}
		defer f.Close() //nolint:errcheck
		in = f
	case len(args) == 0 && !stdin:
		return nil, nil
	}
	var body json.RawMessage
	if err := json.NewDecoder(in).Decode(&body); err != nil {
		return nil, fmt.Errorf("parsing input: %w", err)
	}
	return body, nil
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
