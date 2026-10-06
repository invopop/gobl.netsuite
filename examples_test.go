package netsuite_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invopop/gobl"
	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateOut = flag.Bool("update", false, "update the expected output files in examples/netsuite/out")

const (
	examplesPath = "examples/netsuite"
	outPath      = "examples/netsuite/out"
)

// TestExamples converts every bundle in the examples directory, checks the
// result is valid and matches NetSuite's totals, and compares it with the
// expected output. Run with -update to regenerate the output files.
func TestExamples(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(examplesPath, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, file := range files {
		name := filepath.Base(file)
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			res, err := netsuite.Convert(loadBundle(t, file))
			require.NoError(t, err)

			// Fixed so the output is stable between runs.
			res.Invoice.UUID = uuid.MustParse("0190a63b-6f6c-7d6e-9a2c-3d4e5f60718a")

			env, err := gobl.Envelop(res.Invoice)
			require.NoError(t, err)
			require.NoError(t, env.Calculate())
			require.NoError(t, env.Validate())
			require.NoError(t, res.CheckTotals())

			out, err := json.MarshalIndent(res.Invoice, "", "\t")
			require.NoError(t, err)
			out = append(out, '\n')

			outFile := filepath.Join(outPath, name)
			if *updateOut {
				require.NoError(t, os.MkdirAll(outPath, 0o755))
				require.NoError(t, os.WriteFile(outFile, out, 0o644))
				return
			}
			expected, err := os.ReadFile(outFile)
			require.NoError(t, err, "missing output, run tests with -update")
			assert.JSONEq(t, string(expected), string(out))
		})
	}
}

func loadBundle(t *testing.T, file string) *netsuite.Bundle {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	b := new(netsuite.Bundle)
	require.NoError(t, json.Unmarshal(data, b))
	return b
}
