package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/config"
)

// newConfigCmd builds the `migr config` subcommand.
func newConfigCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the resolved configuration, with secrets redacted",
		Long: "Loads migr.yaml, expands ${VAR} references, applies defaults, and prints the\n" +
			"result. Credentials in connection strings are redacted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.Resolve(opts.configPath)

			opts.log.Debug("loading configuration", "path", path)

			cfg, err := config.Load(path)
			if err != nil {
				return NewExitError(ExitValidation, err)
			}

			return writeConfig(cmd.OutOrStdout(), cfg, opts)
		},
	}
}

// writeConfig renders the resolved config as JSON or YAML.
func writeConfig(w io.Writer, cfg *config.Config, opts *options) error {
	if opts.jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")

		if err := enc.Encode(cfg); err != nil {
			return fmt.Errorf("encoding config: %w", err)
		}

		return nil
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
