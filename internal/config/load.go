package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/rule"
	"github.com/emrebdr/migr/internal/runtime/protocol"
)

// DefaultFilename is the config file looked for when --config is not given.
const DefaultFilename = "migr.yaml"

// DefaultSeedsDir is where seeds live when migr.yaml names no other directory.
const DefaultSeedsDir = "./seeds"

// Load reads, expands, defaults, and validates the config at path.
func Load(path string) (*Config, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no config file at %s (create one with `migr init`): %w", path, err)
		}

		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return parse(src, path, os.LookupEnv)
}

// Resolve returns the config path to load, given an explicit --config value.
func Resolve(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	return DefaultFilename
}

// parse runs the full pipeline over already-read bytes.
func parse(src []byte, source string, lookup func(string) (string, bool)) (*Config, error) {
	file, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing %s:\n%s", source, yaml.FormatError(err, false, true))
	}

	if err := expandEnv(file, lookup); err != nil {
		return nil, fmt.Errorf("expanding variables in %s: %w", source, err)
	}

	cfg := &Config{}
	if err := decode(file, cfg); err != nil {
		return nil, fmt.Errorf("loading %s:\n%s", source, yaml.FormatError(err, false, true))
	}

	applyDefaults(cfg)

	if err := validate(cfg, source); err != nil {
		return nil, err
	}

	resolvePaths(cfg, source)

	return cfg, nil
}

// resolvePaths anchors every relative path in the config to its config file rather than to the cwd.
func resolvePaths(cfg *Config, source string) {
	cfg.Migrations.Dir = anchor(cfg.Migrations.Dir, source)
	cfg.Seeds.Dir = anchor(cfg.Seeds.Dir, source)
}

// anchor joins a relative path to the directory its config file sits in, leaving absolute ones be.
func anchor(target, source string) string {
	if target == "" || filepath.IsAbs(target) {
		return target
	}

	return filepath.Join(filepath.Dir(source), target)
}

// decode converts the expanded document into a Config, rejecting unknown keys.
func decode(file *ast.File, cfg *Config) error {
	for _, doc := range file.Docs {
		if doc.Body == nil {
			continue
		}

		return yaml.NodeToValue(doc.Body, cfg, yaml.DisallowUnknownField())
	}

	return errors.New("config file is empty")
}

// applyDefaults fills every optional field left unset by the document.
func applyDefaults(cfg *Config) {
	setString(&cfg.Migrations.Dir, "./migrations")
	setString(&cfg.History.Table, driver.DefaultHistoryTable)
	setString(&cfg.Seeds.Dir, DefaultSeedsDir)
	setString(&cfg.Seeds.Table, driver.DefaultSeedTable)
	setEnvironments(cfg)
	setDuration(&cfg.Lock.Timeout, driver.DefaultLockWait)
	setDuration(&cfg.Runtime.HandshakeTimeout, protocol.DefaultHandshake)
	setDuration(&cfg.Runtime.RequestTimeout, protocol.DefaultRequest)
	setDuration(&cfg.Runtime.CancelGrace, protocol.DefaultCancelGrace)
	setDuration(&cfg.Runtime.ShutdownGrace, protocol.DefaultShutdownGrace)
	setDuration(&cfg.Safety.LockTimeout, driver.DefaultStatementLockWait)
	setDuration(&cfg.Safety.StatementTimeout, 30*time.Second)
	setDuration(&cfg.Safety.ReplicaLagThreshold, 5*time.Second)
	setDuration(&cfg.Safety.ChunkPause, driver.DefaultChunkPause)

	if cfg.Safety.ChunkRows == nil {
		rows := driver.DefaultChunkRows
		cfg.Safety.ChunkRows = &rows
	}

	if cfg.Safety.LockRetries == 0 {
		cfg.Safety.LockRetries = 5
	}

	if cfg.Safety.MaxLockImpact == "" {
		cfg.Safety.MaxLockImpact = capability.BriefExclusive
	}

	if cfg.Validation.AllowDestructive == nil {
		cfg.Validation.AllowDestructive = []ir.Kind{}
	}

	setRuleDefaults(cfg)
}

// setEnvironments names each database's environment after its own key, where the document named none.
func setEnvironments(cfg *Config) {
	for name, db := range cfg.Databases {
		if db.Environment == "" {
			db.Environment = name
			cfg.Databases[name] = db
		}
	}
}

// setRuleDefaults fills in every rule this build ships, keeping whatever the document said about it.
func setRuleDefaults(cfg *Config) {
	configured := cfg.Validation.Rules
	cfg.Validation.Rules = rule.Defaults()

	for id, severity := range configured {
		cfg.Validation.Rules[id] = severity
	}
}

// setString assigns a default to an empty string field.
func setString(field *string, value string) {
	if *field == "" {
		*field = value
	}
}

// setDuration assigns a default to an unset duration field.
func setDuration(field *Duration, value time.Duration) {
	if *field == 0 {
		*field = Duration(value)
	}
}

// yamlPath renders a node's path without goccy's leading document marker.
func yamlPath(node ast.Node) string {
	return strings.TrimPrefix(node.GetPath(), "$.")
}
