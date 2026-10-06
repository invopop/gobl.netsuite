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

// netsuiteClient builds a client from the environment, using OAuth 2.0
// client credentials (M2M) when NETSUITE_CERTIFICATE_ID is set, or else
// Token-Based Authentication.
func (o *rootOpts) netsuiteClient() (*client.Client, error) {
	var opts []client.Option
	if u := os.Getenv("NETSUITE_BASE_URL"); u != "" {
		opts = append(opts, client.WithBaseURL(u))
	}
	auth, err := authFromEnv()
	if err != nil {
		return nil, err
	}
	return client.New(os.Getenv("NETSUITE_ACCOUNT_ID"), auth, opts...)
}

func authFromEnv() (client.Auth, error) {
	if os.Getenv("NETSUITE_CERTIFICATE_ID") != "" {
		env, err := requireEnv("NETSUITE_ACCOUNT_ID", "NETSUITE_CLIENT_ID", "NETSUITE_CERTIFICATE_ID", "NETSUITE_PRIVATE_KEY_FILE")
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(env["NETSUITE_PRIVATE_KEY_FILE"])
		if err != nil {
			return nil, fmt.Errorf("reading private key: %w", err)
		}
		key, err := client.ParsePrivateKey(data)
		if err != nil {
			return nil, err
		}
		return &client.M2M{
			ClientID:      env["NETSUITE_CLIENT_ID"],
			CertificateID: env["NETSUITE_CERTIFICATE_ID"],
			PrivateKey:    key,
		}, nil
	}
	env, err := requireEnv("NETSUITE_ACCOUNT_ID", "NETSUITE_CONSUMER_KEY", "NETSUITE_CONSUMER_SECRET",
		"NETSUITE_TOKEN_ID", "NETSUITE_TOKEN_SECRET")
	if err != nil {
		return nil, err
	}
	return &client.TBA{
		ConsumerKey:    env["NETSUITE_CONSUMER_KEY"],
		ConsumerSecret: env["NETSUITE_CONSUMER_SECRET"],
		TokenID:        env["NETSUITE_TOKEN_ID"],
		TokenSecret:    env["NETSUITE_TOKEN_SECRET"],
	}, nil
}

// requireEnv reads the environment variables, all of which must be set.
func requireEnv(keys ...string) (map[string]string, error) {
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
	return env, nil
}
