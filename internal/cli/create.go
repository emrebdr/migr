package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/source/sql"
)

// template is the file migr create writes for one language.
type template struct {
	// ext is the extension the file is stored under, which is what names its runtime later.
	ext string
	// body is what the file starts life containing.
	body string
}

// templates holds every language migr create can write a migration in today.
var templates = map[config.Language]template{
	config.LanguageSQL: {ext: sql.Ext, body: sql.Template},
}

// pending names the task that gives each remaining language its SDK and its template.
var pending = map[config.Language]string{
	config.LanguageGo:         "M6-T7",
	config.LanguageTypeScript: "M7-T2",
}

// aliases are the short spellings --lang accepts, which migr.yaml itself does not.
var aliases = map[string]config.Language{"ts": config.LanguageTypeScript}

// newCreateCmd builds the `migr create` subcommand.
func newCreateCmd(opts *options) *cobra.Command {
	var lang string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Write a new migration from a template",
		Long: "Writes a timestamped file into the migrations directory, named as discovery\n" +
			"requires: <YYYYMMDDThhmmss>_<snake_case name>.<ext>, in UTC.",
		Args: argsOf(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.Resolve(opts.configPath))
			if err != nil {
				return NewExitError(ExitValidation, err)
			}

			result, err := create(cfg, opts, args[0], lang)
			if err != nil {
				return err
			}

			return writeResult(cmd.OutOrStdout(), opts, result)
		},
	}

	cmd.Flags().StringVar(&lang, "lang", "",
		"language to write, defaulting to migrations.language in migr.yaml")

	return cmd
}

// created is the migration create wrote, in the form --json reports it.
type created struct {
	// File is the path written, which is the migrations directory plus the whole filename.
	File string `json:"file"`
	// Version is the UTC timestamp it carries, which is what --to and a history row name.
	Version string `json:"version"`
	// Name is the snake_case name it was given.
	Name string `json:"name"`
	// Language is what it was written in, spelled as migr.yaml spells it.
	Language config.Language `json:"language"`
}

// String names the file that was written, which is the whole result.
func (c created) String() string {
	return "created " + c.File + "\n"
}

// create writes one migration, refusing everything discovery would later refuse to read.
func create(cfg *config.Config, opts *options, name, lang string) (created, error) {
	language := resolveLanguage(cfg.Migrations.Language, lang)

	tmpl, err := templateFor(language)
	if err != nil {
		return created{}, NewExitError(ExitValidation, err)
	}

	m, err := migration.New(opts.clock(), name, tmpl.ext)
	if err != nil {
		return created{}, NewExitError(ExitValidation, err)
	}

	if err := checkUnclaimed(cfg.Migrations.Dir, m.Version); err != nil {
		return created{}, err
	}

	path := filepath.Join(cfg.Migrations.Dir, m.Filename())
	if err := writeNew(path, tmpl.body); err != nil {
		return created{}, err
	}

	opts.log.Info("wrote a migration", "file", path, "version", m.Version)

	return created{File: path, Version: m.Version, Name: m.Name, Language: language}, nil
}

// resolveLanguage reads the flag over the config, resolving the short spellings only the CLI takes.
func resolveLanguage(configured config.Language, flag string) config.Language {
	if flag == "" {
		return configured
	}

	if canonical, ok := aliases[flag]; ok {
		return canonical
	}

	return config.Language(flag)
}

// templateFor returns what create writes for a language, or why it cannot write one yet.
func templateFor(lang config.Language) (template, error) {
	if tmpl, ok := templates[lang]; ok {
		return tmpl, nil
	}

	if task, ok := pending[lang]; ok {
		return template{}, fmt.Errorf("migr create cannot write a %s migration yet: that SDK and "+
			"the template it defines arrive in %s. --lang %s is the format M5 runs",
			lang, task, config.LanguageSQL)
	}

	return template{}, fmt.Errorf("%q is not a language migr knows (expected: %s)",
		lang, joinLanguages(config.Languages()))
}

// joinLanguages renders the accepted set for an error message.
func joinLanguages(langs []config.Language) string {
	parts := make([]string, len(langs))
	for i, lang := range langs {
		parts[i] = string(lang)
	}

	return strings.Join(parts, ", ")
}

// checkUnclaimed refuses a version another file already carries, which nothing downstream resolves.
func checkUnclaimed(dir, version string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return unwritable(dir, err)
	}

	if holder := claimant(entries, version); holder != "" {
		return claimed(version, holder)
	}

	return nil
}

// claimant returns the migration in a directory already carrying a version, or empty when none does.
func claimant(entries []fs.DirEntry, version string) string {
	for _, entry := range entries {
		parsed, err := migration.Parse(entry.Name())
		if err == nil && parsed.Version == version {
			return entry.Name()
		}
	}

	return ""
}

// claimed says which file holds the version, and why a second one cannot be written under it.
func claimed(version, holder string) error {
	return fmt.Errorf("%s already claims version %s: versions have second resolution, so two "+
		"migrations created in one second collide, and nothing could then say which one a history "+
		"row records or which one --to names. Try again in a moment", holder, version)
}

// unwritable refuses to write into a migrations directory that is not there, as discovery refuses to read one.
func unwritable(dir string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w (create one with `migr init`): %w", dir, migration.ErrNoDirectory, err)
	}

	return fmt.Errorf("reading %s: %w", dir, err)
}
