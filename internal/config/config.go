package config

import (
	"slices"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/rule"
)

// Version is the only supported migr.yaml schema version.
const Version = 1

// Config is a parsed, defaulted, validated migr.yaml.
type Config struct {
	Version    int                 `yaml:"version"    json:"version"`
	Migrations Migrations          `yaml:"migrations" json:"migrations"`
	Seeds      Seeds               `yaml:"seeds"      json:"seeds"`
	Databases  map[string]Database `yaml:"databases"  json:"databases"`
	History    History             `yaml:"history"    json:"history"`
	Lock       Lock                `yaml:"lock"       json:"lock"`
	Runtime    Runtime             `yaml:"runtime"    json:"runtime"`
	Safety     Safety              `yaml:"safety"     json:"safety"`
	Validation Validation          `yaml:"validation" json:"validation"`
}

// Migrations describes where migrations live and what language they use.
type Migrations struct {
	Dir      string   `yaml:"dir"      json:"dir"`
	Language Language `yaml:"language" json:"language"`
}

// Seeds describes where seeds live and which table records them, apart from the migrations.
type Seeds struct {
	// Dir holds the seeds every environment runs, and one subdirectory per environment for the rest.
	Dir string `yaml:"dir"   json:"dir"`
	// Table records which seeds have run, and is never the table the migration history is in.
	Table string `yaml:"table" json:"table"`
	// Environments names every environment a subdirectory may be for, where databases name fewer.
	Environments []string `yaml:"environments" json:"environments,omitempty"`
}

// Database is a single migration target.
type Database struct {
	Driver string `yaml:"driver" json:"driver"`
	DSN    Secret `yaml:"dsn"    json:"dsn"`
	Schema string `yaml:"schema" json:"schema,omitempty"`
	// Environment names the seeds subdirectory this database also runs, defaulting to its own key.
	Environment string `yaml:"environment" json:"environment"`
}

// History configures where applied migrations are recorded.
type History struct {
	Table string `yaml:"table" json:"table"`
}

// Lock configures the migration lock that prevents concurrent runs.
type Lock struct {
	// Strategy is how the lock is taken, and nothing means whichever one the driver implements.
	Strategy LockStrategy `yaml:"strategy" json:"strategy,omitempty"`
	// Timeout bounds the wait for it, which every engine has some way of spelling.
	Timeout Duration `yaml:"timeout"  json:"timeout"`
}

// Runtime bounds the conversation with a language runtime, which docs/protocol.md §8.2 tabulates.
type Runtime struct {
	HandshakeTimeout Duration `yaml:"handshake_timeout" json:"handshake_timeout"`
	RequestTimeout   Duration `yaml:"request_timeout"   json:"request_timeout"`
	CancelGrace      Duration `yaml:"cancel_grace"      json:"cancel_grace"`
	ShutdownGrace    Duration `yaml:"shutdown_grace"    json:"shutdown_grace"`
}

// Safety holds the thresholds that gate zero-downtime checks.
type Safety struct {
	LockTimeout         Duration              `yaml:"lock_timeout"          json:"lock_timeout"`
	LockRetries         *int                  `yaml:"lock_retries"          json:"lock_retries"`
	LockBackoff         Duration              `yaml:"lock_backoff"          json:"lock_backoff"`
	StatementTimeout    Duration              `yaml:"statement_timeout"     json:"statement_timeout"`
	MaxLockImpact       capability.LockImpact `yaml:"max_lock_impact"       json:"max_lock_impact"`
	AutoRewrite         bool                  `yaml:"auto_rewrite"          json:"auto_rewrite"`
	ReplicaLagThreshold Duration              `yaml:"replica_lag_threshold" json:"replica_lag_threshold"`
	ChunkRows           *int                  `yaml:"chunk_rows"            json:"chunk_rows"`
	ChunkPause          Duration              `yaml:"chunk_pause"           json:"chunk_pause"`
}

// Retries returns how many times a statement that gave up on a lock is tried again, zero none.
func (s Safety) Retries() int {
	if s.LockRetries == nil {
		return driver.DefaultLockRetries
	}

	return *s.LockRetries
}

// Rows returns how many rows a chunk of a data-only migration covers, zero running none in chunks.
func (s Safety) Rows() int {
	if s.ChunkRows == nil {
		return driver.DefaultChunkRows
	}

	return *s.ChunkRows
}

// Validation configures pre-execution checks.
type Validation struct {
	AllowDestructive []ir.Kind                 `yaml:"allow_destructive" json:"allow_destructive"`
	Rules            map[rule.ID]rule.Severity `yaml:"rules"             json:"rules"`
}

// Language identifies the runtime that executes migrations.
type Language string

// Supported migration languages. The SDK-backed ones arrive with their runtimes in M6 and M7.
const (
	LanguageSQL        Language = "sql"
	LanguageGo         Language = "go"
	LanguageTypeScript Language = "typescript"
)

// LockStrategy selects how the migration lock is acquired.
type LockStrategy string

// Supported lock strategies.
const (
	LockAdvisory LockStrategy = "advisory"
	LockTable    LockStrategy = "table"
	LockFile     LockStrategy = "file"
)

// languages lists every accepted Language, for validation and error text.
var languages = []Language{LanguageSQL, LanguageGo, LanguageTypeScript}

// Languages returns every accepted Language, so a caller listing them cannot list a different set.
func Languages() []Language {
	return slices.Clone(languages)
}

// lockStrategies lists every accepted LockStrategy.
var lockStrategies = []LockStrategy{LockAdvisory, LockTable, LockFile}
