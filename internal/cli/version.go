package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/version"
)

// newVersionCmd builds the `migr version` subcommand.
func newVersionCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and platform",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeVersion(cmd.OutOrStdout(), version.Get(), opts)
		},
	}
}

// writeVersion renders build info as JSON or as a single human-readable line.
func writeVersion(w io.Writer, info version.Info, opts *options) error {
	opts.log.Info("resolved build information", "version", info.Version, "commit", info.Commit)

	if opts.jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")

		if err := enc.Encode(info); err != nil {
			return fmt.Errorf("encoding version: %w", err)
		}

		return nil
	}

	if _, err := fmt.Fprintln(w, info.String()); err != nil {
		return fmt.Errorf("writing version: %w", err)
	}

	return nil
}
