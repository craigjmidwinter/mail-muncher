package main

import (
	"fmt"
	"io"

	"github.com/craigjmidwinter/mail-muncher/internal/config"
	"github.com/craigjmidwinter/mail-muncher/internal/filter"
	"github.com/spf13/cobra"
)

// init installs the match-tree compiler as config's validation hook, so every
// path into config.Validate — `validate`, and run/daemon startup — reports a
// malformed match tree (unknown predicate, two keys in one node, bad regex or
// duration) against the rule that contains it.
func init() {
	config.MatchValidator = func(r *config.Rule) error {
		_, err := filter.Compile(&r.Match)
		return err
	}
}

// newValidateCommand builds the `validate` subcommand: parse the config,
// resolve referenced files, and report problems.
//
// Every problem is printed, errors first. The command exits non-zero if any
// problem is error-severity; warnings alone exit zero.
func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Parse the config, resolve referenced files, and report problems",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Problems reported here are the user's config, not their
			// command line; usage output would only be noise.
			cmd.SilenceUsage = true

			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			// "There is no config file" is a different state from "the config
			// file is broken", and only one of them has a next command.
			if err := requireConfigFile(path); err != nil {
				return err
			}

			cfg, err := config.Load(path)
			if err != nil {
				return err
			}

			return reportProblems(cmd.OutOrStdout(), cfg, config.Validate(cfg))
		},
	}
}

// reportProblems writes a validation report and returns a non-nil error when
// the config has error-severity problems.
func reportProblems(w io.Writer, cfg *config.Config, problems config.Problems) error {
	errs := problems.Errors()
	warnings := problems.Warnings()

	fmt.Fprintf(w, "config: %s\n", cfg.Path)
	fmt.Fprintf(w, "%d account(s), %d rule(s), state_dir %s\n", len(cfg.Accounts), len(cfg.Rules), cfg.StateDir)

	for _, p := range errs {
		fmt.Fprintln(w, p)
	}
	for _, p := range warnings {
		fmt.Fprintln(w, p)
	}

	switch {
	case len(errs) > 0:
		fmt.Fprintf(w, "FAILED: %s, %s\n", plural(len(errs), "error"), plural(len(warnings), "warning"))
		return fmt.Errorf("config is invalid: %s", plural(len(errs), "error"))
	case len(warnings) > 0:
		fmt.Fprintf(w, "OK with %s\n", plural(len(warnings), "warning"))
	default:
		fmt.Fprintln(w, "OK")
	}

	return nil
}

// plural renders "1 error" / "2 errors".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
