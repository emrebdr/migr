package config

import (
	"fmt"
	"sort"
	"strings"
)

// FieldError is one problem, located by its path within the YAML document.
type FieldError struct {
	Path    string
	Message string
}

// Error renders the problem as "path: message".
func (e FieldError) Error() string {
	return e.Path + ": " + e.Message
}

// ValidationError collects every problem found in one config file.
type ValidationError struct {
	Source string
	Errors []FieldError
}

// Error lists every problem, one per line, sorted by path.
func (e *ValidationError) Error() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s has %s:", e.Source, pluralProblems(len(e.Errors)))

	width := longestPath(e.Errors)
	for _, fe := range e.Errors {
		fmt.Fprintf(&b, "\n  %-*s  %s", width, fe.Path, fe.Message)
	}

	return b.String()
}

// pluralProblems renders the problem count with the right noun.
func pluralProblems(n int) string {
	if n == 1 {
		return "1 problem"
	}

	return fmt.Sprintf("%d problems", n)
}

// longestPath returns the width needed to align every path in a report.
func longestPath(errs []FieldError) int {
	width := 0
	for _, fe := range errs {
		if len(fe.Path) > width {
			width = len(fe.Path)
		}
	}

	return width
}

// validator accumulates field problems so every one is reported at once.
type validator struct {
	errs []FieldError
}

// add records a problem at the given path.
func (v *validator) add(path, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// require records a problem when a string field is empty.
func (v *validator) require(path, value string) bool {
	if strings.TrimSpace(value) == "" {
		v.add(path, "required field is missing")

		return false
	}

	return true
}

// validate checks a defaulted config, reporting every problem it finds.
func validate(cfg *Config, source string) error {
	v := &validator{}

	if cfg.Version != Version {
		v.add("version", "unsupported config version %d (expected %d)", cfg.Version, Version)
	}

	validateMigrations(v, cfg.Migrations)
	validateDatabases(v, cfg.Databases)
	validateLock(v, cfg.Lock)
	validateSafety(v, cfg.Safety)

	if len(v.errs) == 0 {
		return nil
	}

	sort.Slice(v.errs, func(i, j int) bool { return v.errs[i].Path < v.errs[j].Path })

	return &ValidationError{Source: source, Errors: v.errs}
}

// validateMigrations checks the migrations block.
func validateMigrations(v *validator, m Migrations) {
	v.require("migrations.dir", m.Dir)

	if !v.require("migrations.language", string(m.Language)) {
		return
	}

	if !contains(languages, m.Language) {
		v.add("migrations.language", "%q is not valid (expected: %s)", m.Language, join(languages))
	}
}

// validateDatabases checks that at least one usable database is defined.
func validateDatabases(v *validator, dbs map[string]Database) {
	if len(dbs) == 0 {
		v.add("databases", "at least one database must be defined")

		return
	}

	for _, name := range sortedKeys(dbs) {
		db := dbs[name]
		v.require("databases."+name+".driver", db.Driver)
		v.require("databases."+name+".dsn", db.DSN.Reveal())
	}
}

// validateLock checks the migration lock block.
func validateLock(v *validator, l Lock) {
	if !contains(lockStrategies, l.Strategy) {
		v.add("lock.strategy", "%q is not valid (expected: %s)", l.Strategy, join(lockStrategies))
	}

	if l.Timeout <= 0 {
		v.add("lock.timeout", "must be positive, got %s", l.Timeout)
	}
}

// validateSafety checks the zero-downtime thresholds.
func validateSafety(v *validator, s Safety) {
	if !contains(lockImpacts, s.MaxLockImpact) {
		v.add("safety.max_lock_impact", "%q is not valid (expected: %s)", s.MaxLockImpact, join(lockImpacts))
	}

	if s.LockTimeout <= 0 {
		v.add("safety.lock_timeout", "must be positive, got %s", s.LockTimeout)
	}

	if s.LockRetries < 0 {
		v.add("safety.lock_retries", "must not be negative, got %d", s.LockRetries)
	}

	if s.StatementTimeout <= 0 {
		v.add("safety.statement_timeout", "must be positive, got %s", s.StatementTimeout)
	}

	if s.ReplicaLagThreshold <= 0 {
		v.add("safety.replica_lag_threshold", "must be positive, got %s", s.ReplicaLagThreshold)
	}
}

// contains reports whether v appears in the allowed set.
func contains[T comparable](allowed []T, v T) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}

	return false
}

// join renders an allowed set for an error message.
func join[T ~string](allowed []T) string {
	parts := make([]string, len(allowed))
	for i, a := range allowed {
		parts[i] = string(a)
	}

	return strings.Join(parts, ", ")
}

// sortedKeys returns a map's keys in a deterministic order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
