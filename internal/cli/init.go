package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/config"
)

// scaffoldDir is the migrations directory init creates, and the one the config it writes names.
const scaffoldDir = "migrations"

// scaffoldConfig is a migr.yaml holding every key that has no default, and nothing that has one.
const scaffoldConfig = `# Written by migr init. Every other setting has a default, and migr config
# prints the resolved set with secrets redacted.
version: 1
migrations:
  dir: ./` + scaffoldDir + `
  language: ` + string(config.LanguageSQL) + `
databases:
  default:
    driver: postgres
    # The environment wins; the fallback is a development database that does not
    # exist yet, so nothing connects anywhere by accident before you set this.
    dsn: ${DATABASE_URL:-` + scaffoldDSN + `}
`

// scaffoldDSN is what the scaffold falls back to, and it is a placeholder rather than a database.
const scaffoldDSN = "postgres://localhost:5432/mydb"

// nextStep is what to do with a scaffolded project, and what is still owed before it connects.
const nextStep = "next: `migr create add_users`, and set DATABASE_URL before `migr up`\n"

// newInitCmd builds the `migr init` subcommand.
func newInitCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold migr.yaml and a migrations directory",
		Long: "Writes a migr.yaml holding the settings that have no default, and creates the\n" +
			"migrations directory beside it. An existing config is never overwritten.",
		Args: argsOf(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.Resolve(opts.configPath)

			opts.log.Debug("scaffolding a project", "config", path)

			result, err := scaffold(path)
			if err != nil {
				return err
			}

			return writeResult(cmd.OutOrStdout(), opts, result)
		},
	}
}

// scaffolded is what init wrote, in the form --json reports it.
type scaffolded struct {
	// Config is the path the config file was written to.
	Config string `json:"config"`
	// MigrationsDir is the migrations directory, anchored beside that file as the loader anchors it.
	MigrationsDir string `json:"migrations_dir"`
	// Created names what this run made, which is everything that was not already there.
	Created []string `json:"created"`
}

// String lists what was created, what was already there, and the step that is still owed.
func (s scaffolded) String() string {
	var b strings.Builder

	for _, path := range s.Created {
		fmt.Fprintf(&b, "created %s\n", path)
	}

	if !slices.Contains(s.Created, s.MigrationsDir) {
		fmt.Fprintf(&b, "%s already exists\n", s.MigrationsDir)
	}

	b.WriteString(nextStep)

	return b.String()
}

// scaffold writes the config and the directory it names, refusing a project already initialized.
func scaffold(path string) (scaffolded, error) {
	if err := checkAbsent(path); err != nil {
		return scaffolded{}, err
	}

	dir := filepath.Join(filepath.Dir(path), scaffoldDir)

	made, err := ensureDir(dir)
	if err != nil {
		return scaffolded{}, err
	}

	if err := writeNew(path, scaffoldConfig); err != nil {
		return scaffolded{}, err
	}

	return scaffolded{Config: path, MigrationsDir: dir, Created: append([]string{path}, made...)}, nil
}

// checkAbsent refuses to touch a project that already has a config, before anything is written.
func checkAbsent(path string) error {
	_, err := os.Stat(path)
	if err == nil {
		return alreadyInitialized(path)
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", path, err)
	}

	return nil
}

// alreadyInitialized says why an existing config is never replaced, and where one may be written.
func alreadyInitialized(path string) error {
	return fmt.Errorf("%s already exists, and init will not overwrite it: that file names your "+
		"databases, and no flag here replaces one. Delete it, or pass --config to write elsewhere",
		path)
}

// ensureDir creates the migrations directory, reporting it as created only when it was not there.
func ensureDir(dir string) ([]string, error) {
	_, err := os.Stat(dir)
	if err == nil {
		return nil, nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("checking %s: %w", dir, err)
	}

	if err := os.MkdirAll(dir, newDirMode); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}

	return []string{dir}, nil
}
