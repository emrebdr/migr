package cli

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/runtime"
)

// options holds the values bound to the root command's persistent flags.
type options struct {
	configPath       string
	database         string
	jsonOut          bool
	verbosity        int
	allowDestructive bool
	log              *slog.Logger
	clock            func() time.Time
	drivers          *driver.Registry
	runtimes         *runtime.Registry
}

// Execute builds the root command and runs it against the given arguments.
func Execute(ctx context.Context, drivers *driver.Registry, runtimes *runtime.Registry,
	args []string) error {
	opts := &options{drivers: drivers, runtimes: runtimes}
	cmd := rootCmd(opts)
	cmd.SetArgs(args)

	return execute(ctx, cmd, opts)
}

// execute runs the command tree and reports whatever failed it, in the form --json promises.
func execute(ctx context.Context, cmd *cobra.Command, opts *options) error {
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return nil
	}

	if written := reportFailure(cmd.ErrOrStderr(), opts, err); written != nil {
		return errors.Join(err, written)
	}

	return err
}

// newRootCmd constructs a fresh root command with no drivers, for the commands that reach no database.
func newRootCmd() *cobra.Command {
	return rootCmd(&options{})
}

// rootCmd builds the tree around options already made, which is how a test fixes the clock.
func rootCmd(opts *options) *cobra.Command {
	if opts.clock == nil {
		opts.clock = time.Now
	}

	cmd := &cobra.Command{
		Use:   "migr",
		Short: "Polyglot, database-agnostic migration framework",
		Long: "migr runs database migrations written as code in your own language against\n" +
			"PostgreSQL, MySQL, SQLite, and more, through one migration workflow.",
		Args:          argsOf(cobra.NoArgs),
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.SetFlagErrorFunc(flagRefused)
	registerGlobalFlags(cmd, opts)
	cmd.AddCommand(newVersionCmd(opts), newConfigCmd(opts), newInitCmd(opts), newCreateCmd(opts),
		newUpCmd(opts), newDownCmd(opts), newStatusCmd(opts), newHistoryCmd(opts),
		newPlanCmd(opts), newValidateCmd(opts), newResolveCmd(opts))

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

// registerAllowDestructive binds the permission flag, on the two commands that read it and no others.
func registerAllowDestructive(cmd *cobra.Command, opts *options) {
	cmd.Flags().BoolVar(&opts.allowDestructive, "allow-destructive", false,
		"permit every operation that destroys data, for this run")
}
