package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/invopop/gobl.netsuite/client"
	"github.com/spf13/cobra"
)

type rootOpts struct {
}

func root() *rootOpts {
	return &rootOpts{}
}

func (o *rootOpts) cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           name,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(versionCmd())
	cmd.AddCommand(probe(o).cmd())
	cmd.AddCommand(convert(o).cmd())
	cmd.AddCommand(presetsCmd())

	return cmd
}

// netsuiteClient builds a client using TBA credentials from the environment.
func (o *rootOpts) netsuiteClient() (*client.Client, error) {
	keys := []string{
		"NETSUITE_ACCOUNT_ID",
		"NETSUITE_CONSUMER_KEY",
		"NETSUITE_CONSUMER_SECRET",
		"NETSUITE_TOKEN_ID",
		"NETSUITE_TOKEN_SECRET",
	}
	env := make(map[string]string, len(keys))
	var missing []string
	for _, k := range keys {
		env[k] = os.Getenv(k)
		if env[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}

	var opts []client.Option
	if u := os.Getenv("NETSUITE_BASE_URL"); u != "" {
		opts = append(opts, client.WithBaseURL(u))
	}

	return client.New(env["NETSUITE_ACCOUNT_ID"], &client.TBA{
		ConsumerKey:    env["NETSUITE_CONSUMER_KEY"],
		ConsumerSecret: env["NETSUITE_CONSUMER_SECRET"],
		TokenID:        env["NETSUITE_TOKEN_ID"],
		TokenSecret:    env["NETSUITE_TOKEN_SECRET"],
	}, opts...)
}
