package cli

import (
	"context"
	"log/slog"

	"github.com/spf13/cobra"
)

// options holds the values bound to the root command's persistent flags.
type options struct {
	configPath string
	database   string
	jsonOut    bool
	verbosity  int
	log        *slog.Logger
}

// Execute builds the root command and runs it against the given arguments.
func Execute(ctx context.Context, args []string) error {
	cmd := newRootCmd()
	cmd.SetArgs(args)

	return cmd.ExecuteContext(ctx)
}

// newRootCmd constructs a fresh root command with all subcommands attached.
func newRootCmd() *cobra.Command {
	opts := &options{}

	cmd := &cobra.Command{
		Use:   "migr",
		Short: "Polyglot, database-agnostic migration framework",
		Long: "migr runs database migrations written as code in your own language against\n" +
			"PostgreSQL, MySQL, SQLite, and more, through one migration workflow.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			opts.log = newLogger(opts, cmd.ErrOrStderr())
			opts.log.Debug("cli started",
				"config", opts.configPath,
				"database", opts.database,
				"json", opts.jsonOut,
				"verbosity", opts.verbosity,
			)

			return nil
		},
	}

	registerGlobalFlags(cmd, opts)
	cmd.AddCommand(newVersionCmd(opts))

	return cmd
}

// registerGlobalFlags binds the persistent flags shared by every subcommand.
func registerGlobalFlags(cmd *cobra.Command, opts *options) {
	f := cmd.PersistentFlags()
	f.StringVar(&opts.configPath, "config", "", "path to migr.yaml (default: ./migr.yaml)")
	f.StringVar(&opts.database, "database", "", "database to target, by name in migr.yaml")
	f.BoolVar(&opts.jsonOut, "json", false, "emit JSON results on stdout and JSON logs on stderr")
	f.CountVarP(&opts.verbosity, "verbose", "v", "increase log detail (-v info, -vv debug)")
}
