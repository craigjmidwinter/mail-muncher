package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/pipeline"
	"github.com/craigjmidwinter/mail-muncher/internal/provider/gmail"
	"github.com/spf13/cobra"
)

// newAuthCommand builds the `auth` subcommand: interactive provider
// authentication.
//
// For Gmail this is the loopback OAuth flow — the command prints a consent
// URL, waits for the browser to redirect back to a local port, and caches the
// resulting token at the account's `gmail.token_file`. It is the one command
// that is expected to be run by hand; everything else runs unattended off the
// token it writes.
func newAuthCommand() *cobra.Command {
	var account string

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate interactively against a mail provider",
		Long: "Authenticate interactively against a mail provider and cache the credentials.\n\n" +
			"Gmail accounts use the OAuth loopback flow: a consent URL is printed (and opened\n" +
			"in a browser where possible) and the resulting token is written to the account's\n" +
			"token_file with mode 0600. Re-run this command if a token is ever revoked.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Problems here are the user's config or their Google account,
			// not their command line; usage output would only be noise.
			cmd.SilenceUsage = true

			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			// An unconfigured machine is the common case for `auth` — it is the
			// command people reach for first — so it gets the same setup
			// guidance every other command gives rather than an open(2) failure.
			if err := requireConfigFile(path); err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}

			acct, err := resolveAccount(cfg, account)
			if err != nil {
				return err
			}

			opts, err := gmail.OptionsFromAccount(acct)
			if err != nil {
				return err
			}

			auth, err := gmail.NewAuthorizer(opts)
			if err != nil {
				return err
			}
			auth.Out = cmd.OutOrStdout()

			if _, err := auth.Run(cmd.Context()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Authorized %q; token written to %s\n", opts.Account, opts.TokenFile)
			return nil
		},
	}

	cmd.Flags().StringVar(&account, "account", "", "name of the account to authenticate (required when the config has more than one)")

	return cmd
}

// resolveAccount picks the account to authenticate: the named one, or the only
// one configured when --account was omitted.
func resolveAccount(cfg *config.Config, name string) (*config.Account, error) {
	if len(cfg.Accounts) == 0 {
		return nil, setupFailure(pipeline.ExitConfig, &setupError{
			Kind: kindNoAccounts,
			Path: cfg.Path,
			Text: noAccountsGuidance(cfg.Path, errors.New("the accounts: list is empty")),
		})
	}

	if strings.TrimSpace(name) == "" {
		if len(cfg.Accounts) == 1 {
			return &cfg.Accounts[0], nil
		}
		return nil, fmt.Errorf("--account is required; %s configures %s",
			cfg.Path, accountNames(cfg))
	}

	acct := cfg.Account(name)
	if acct == nil {
		return nil, fmt.Errorf("no account named %q in %s; configured accounts: %s",
			name, cfg.Path, accountNames(cfg))
	}
	return acct, nil
}

// accountNames renders the configured account names for an error message.
func accountNames(cfg *config.Config) string {
	names := make([]string, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		names = append(names, fmt.Sprintf("%q", a.Name))
	}
	return strings.Join(names, ", ")
}
