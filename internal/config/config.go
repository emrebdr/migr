package config

// Version is the only supported migr.yaml schema version.
const Version = 1

// Config is a parsed, defaulted, validated migr.yaml.
type Config struct {
	Version    int                 `yaml:"version"    json:"version"`
	Migrations Migrations          `yaml:"migrations" json:"migrations"`
	Databases  map[string]Database `yaml:"databases"  json:"databases"`
	History    History             `yaml:"history"    json:"history"`
	Lock       Lock                `yaml:"lock"       json:"lock"`
	Safety     Safety              `yaml:"safety"     json:"safety"`
	Validation Validation          `yaml:"validation" json:"validation"`
}

// Migrations describes where migrations live and what language they use.
type Migrations struct {
	Dir      string   `yaml:"dir"      json:"dir"`
	Language Language `yaml:"language" json:"language"`
}

// Database is a single migration target.
type Database struct {
	Driver string `yaml:"driver" json:"driver"`
	DSN    Secret `yaml:"dsn"    json:"dsn"`
	Schema string `yaml:"schema" json:"schema,omitempty"`
}

// History configures where applied migrations are recorded.
type History struct {
	Table string `yaml:"table" json:"table"`
}

// Lock configures the migration lock that prevents concurrent runs.
type Lock struct {
	Strategy LockStrategy `yaml:"strategy" json:"strategy"`
	Timeout  Duration     `yaml:"timeout"  json:"timeout"`
}

// Safety holds the thresholds that gate zero-downtime checks.
type Safety struct {
	LockTimeout         Duration   `yaml:"lock_timeout"          json:"lock_timeout"`
	LockRetries         int        `yaml:"lock_retries"          json:"lock_retries"`
	StatementTimeout    Duration   `yaml:"statement_timeout"     json:"statement_timeout"`
	MaxLockImpact       LockImpact `yaml:"max_lock_impact"       json:"max_lock_impact"`
	AutoRewrite         bool       `yaml:"auto_rewrite"          json:"auto_rewrite"`
	ReplicaLagThreshold Duration   `yaml:"replica_lag_threshold" json:"replica_lag_threshold"`
}

// Validation configures pre-execution checks.
type Validation struct {
	AllowDestructive bool `yaml:"allow_destructive" json:"allow_destructive"`
}

// Language identifies the runtime that executes migrations.
type Language string

// Supported migration languages. More arrive with their SDKs in M6 and M7.
const (
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

// LockImpact is the cost an operation imposes on a live database.
type LockImpact string

// Lock impact verdicts, ordered from cheapest to most disruptive.
const (
	LockFree       LockImpact = "lock_free"
	BriefExclusive LockImpact = "brief_exclusive"
	LongExclusive  LockImpact = "long_exclusive"
	TableRewrite   LockImpact = "table_rewrite"
)

// languages lists every accepted Language, for validation and error text.
var languages = []Language{LanguageGo, LanguageTypeScript}

// lockStrategies lists every accepted LockStrategy.
var lockStrategies = []LockStrategy{LockAdvisory, LockTable, LockFile}

// lockImpacts lists every accepted LockImpact, cheapest first.
var lockImpacts = []LockImpact{LockFree, BriefExclusive, LongExclusive, TableRewrite}
