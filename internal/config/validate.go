package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/rule"
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
	validateSeeds(v, cfg, source)
	validateDatabases(v, cfg.Databases)
	validateLock(v, cfg.Lock)
	validateRuntime(v, cfg.Runtime)
	validateSafety(v, cfg.Safety)
	validateValidation(v, cfg.Validation)

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
		validateEnvironment(v, "databases."+name+".environment", db.Environment)
		validateReplicas(v, "databases."+name+".replicas", db.Replicas)
	}
}

// validateReplicas refuses a replica named by nothing, which no driver could connect to.
func validateReplicas(v *validator, path string, replicas []Secret) {
	for i, r := range replicas {
		if strings.TrimSpace(r.Reveal()) == "" {
			v.add(fmt.Sprintf("%s[%d]", path, i), "is blank, and a replica is a connection string")
		}
	}
}

// validateEnvironment refuses an environment that cannot name one directory beside the seeds.
func validateEnvironment(v *validator, path, env string) {
	if reason := unnameable(env); reason != "" {
		v.add(path, "%q names the seeds subdirectory this database runs, and %s -- it defaults "+
			"to the database's own key, so set it explicitly", env, reason)
	}
}

// unnameable says why a name cannot be one directory directly inside another, and nothing when it can.
func unnameable(name string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "is blank"
	case strings.ContainsAny(name, `/\`):
		return "a separator would make it a path rather than a directory"
	case strings.HasPrefix(name, "."):
		return "a leading dot is a file discovery passes over, so its seeds would never run"
	default:
		return ""
	}
}

// validateSeeds checks the seeds block, which may share neither a table nor a directory with migrations.
func validateSeeds(v *validator, cfg *Config, source string) {
	v.require("seeds.dir", cfg.Seeds.Dir)

	if v.require("seeds.table", cfg.Seeds.Table) && cfg.Seeds.Table == cfg.History.Table {
		v.add("seeds.table", "%q is history.table too, and seeds keep a history of their own so "+
			"that no seed is ever recorded as a migration", cfg.Seeds.Table)
	}

	validateProgressNames(v, cfg.History.Table, cfg.Seeds.Table)

	validateDeclaredEnvironments(v, cfg)

	if cfg.Seeds.Dir == "" || cfg.Migrations.Dir == "" {
		return
	}

	if overlap := overlapping(anchor(cfg.Seeds.Dir, source),
		anchor(cfg.Migrations.Dir, source)); overlap != "" {
		v.add("seeds.dir", "%q %s migrations.dir %q, and seeds never mix with migrations",
			cfg.Seeds.Dir, overlap, cfg.Migrations.Dir)
	}
}

// validateProgressNames refuses a history or seed table named as a table migr keeps beside the other.
func validateProgressNames(v *validator, migrations, seeds string) {
	for _, beside := range besides {
		if seeds != "" && seeds == beside.name(migrations) {
			v.add("seeds.table", "%q is where history.table's %s are kept", seeds, beside.holds)
		}

		if migrations != "" && migrations == beside.name(seeds) {
			v.add("history.table", "%q is where seeds.table's %s are kept", migrations, beside.holds)
		}
	}
}

// beside is one table migr keeps next to a history table, and what it holds.
type beside struct {
	// name derives the table's name from the history table's.
	name func(table string) string
	// holds says what the table keeps, as a refusal names it.
	holds string
}

// besides are the tables migr keeps next to every history table.
var besides = []beside{
	{name: history.ProgressTable, holds: "checkpoints"},
	{name: history.ReleaseTable, holds: "releases"},
	{name: history.ShadowTable, holds: "shadow-table migrations"},
}

// validateDeclaredEnvironments checks the declared environments, which every database must be one of.
func validateDeclaredEnvironments(v *validator, cfg *Config) {
	declared := cfg.Seeds.Environments
	if len(declared) == 0 {
		return
	}

	for i, env := range declared {
		path := fmt.Sprintf("seeds.environments[%d]", i)

		if reason := unnameable(env); reason != "" {
			v.add(path, "%q names a seeds subdirectory, and %s", env, reason)
		} else if slices.Contains(declared[:i], env) {
			v.add(path, "%q is declared twice, and once is what declares it", env)
		}
	}

	for _, name := range sortedKeys(cfg.Databases) {
		env := cfg.Databases[name].Environment
		if env != "" && !slices.Contains(declared, env) {
			v.add("databases."+name+".environment", "%q is not one of seeds.environments (%s), "+
				"so its seeds directory would be refused as one no environment runs -- it "+
				"defaults to the database's own key, so set it explicitly", env,
				strings.Join(declared, ", "))
		}
	}
}

// overlapping says how one directory shares files with another, and nothing when the two are apart.
func overlapping(seeds, migrations string) string {
	seeds, migrations = filepath.Clean(seeds), filepath.Clean(migrations)

	switch {
	case seeds == migrations:
		return "is"
	case within(seeds, migrations):
		return "is inside"
	case within(migrations, seeds):
		return "holds"
	default:
		return ""
	}
}

// within reports whether one cleaned path lies beneath another.
func within(inner, outer string) bool {
	rel, err := filepath.Rel(outer, inner)

	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// validateLock checks the migration lock block, where naming no strategy leaves it to the driver.
func validateLock(v *validator, l Lock) {
	if l.Strategy != "" && !contains(lockStrategies, l.Strategy) {
		v.add("lock.strategy", "%q is not valid (expected: %s, or nothing to leave it to the driver)",
			l.Strategy, join(lockStrategies))
	}

	if l.Timeout <= 0 {
		v.add("lock.timeout", "must be positive, got %s", l.Timeout)
	}
}

// validateRuntime checks §8.2's bounds, none of which may be the zero that means forever.
func validateRuntime(v *validator, r Runtime) {
	if r.HandshakeTimeout <= 0 {
		v.add("runtime.handshake_timeout", "must be positive, got %s", r.HandshakeTimeout)
	}

	if r.RequestTimeout <= 0 {
		v.add("runtime.request_timeout", "must be positive, got %s", r.RequestTimeout)
	}

	if r.CancelGrace <= 0 {
		v.add("runtime.cancel_grace", "must be positive, got %s", r.CancelGrace)
	}

	if r.ShutdownGrace <= 0 {
		v.add("runtime.shutdown_grace", "must be positive, got %s", r.ShutdownGrace)
	}
}

// validateSafety checks the zero-downtime thresholds.
func validateSafety(v *validator, s Safety) {
	if !contains(capability.Ceilings(), s.MaxLockImpact) {
		v.add("safety.max_lock_impact", "%q is not valid (expected: %s)",
			string(s.MaxLockImpact), join(capability.Ceilings()))
	}

	if s.LockTimeout <= 0 {
		v.add("safety.lock_timeout", "must be positive, got %s", s.LockTimeout)
	}

	if s.Retries() < 0 {
		v.add("safety.lock_retries", "must not be negative, got %d; 0 tries a statement that gave "+
			"up on a lock only once", s.Retries())
	}

	if s.LockBackoff <= 0 {
		v.add("safety.lock_backoff", "must be positive, got %s: a retry with no backoff goes "+
			"straight back into the queue it timed out of", s.LockBackoff)
	}

	if s.StatementTimeout <= 0 {
		v.add("safety.statement_timeout", "must be positive, got %s", s.StatementTimeout)
	}

	if s.StatementTimeout > 0 && s.LockTimeout > 0 && s.StatementTimeout <= s.LockTimeout {
		v.add("safety.statement_timeout", "must be longer than safety.lock_timeout (%s), got %s: "+
			"otherwise a statement waiting for a lock is stopped as too slow before its lock wait "+
			"ends, and is never retried", s.LockTimeout, s.StatementTimeout)
	}

	validateMigrationTimeout(v, s)
	validateConcurrentTimeout(v, s)
	validateRewrites(v, s.Rewrites)

	if s.ReplicaLagThreshold <= 0 {
		v.add("safety.replica_lag_threshold", "must be positive, got %s", s.ReplicaLagThreshold)
	}

	if s.Rows() < 0 {
		v.add("safety.chunk_rows", "must not be negative, got %d; 0 runs every data operation as "+
			"one statement", s.Rows())
	}

	if s.ChunkPause <= 0 {
		v.add("safety.chunk_pause", "must be positive, got %s: chunks run back to back are an "+
			"unthrottled backfill, which is itself an outage", s.ChunkPause)
	}
}

// validateMigrationTimeout checks the optional bound on one migration against the bound on one statement.
func validateMigrationTimeout(v *validator, s Safety) {
	if s.MigrationTimeout == nil {
		return
	}

	limit := s.Migration()

	switch {
	case limit <= 0:
		v.add("safety.migration_timeout", "must be positive, got %s; leave it unset for no bound",
			*s.MigrationTimeout)
	case limit < s.StatementTimeout.Duration():
		v.add("safety.migration_timeout", "must be at least safety.statement_timeout (%s), got %s: "+
			"a migration is at least one statement", s.StatementTimeout, *s.MigrationTimeout)
	}
}

// validateConcurrentTimeout checks the optional bound on a statement that blocks no write against its lock wait.
func validateConcurrentTimeout(v *validator, s Safety) {
	if s.ConcurrentTimeout == nil {
		return
	}

	limit := s.Concurrent()

	switch {
	case limit <= 0:
		v.add("safety.concurrent_timeout", "must be positive, got %s; leave it unset for no bound",
			*s.ConcurrentTimeout)
	case s.LockTimeout > 0 && limit <= s.LockTimeout.Duration():
		v.add("safety.concurrent_timeout", "must be longer than safety.lock_timeout (%s), got %s: "+
			"a concurrent build waits for its lock first, and would be stopped as too slow before "+
			"that wait ends", s.LockTimeout, *s.ConcurrentTimeout)
	}
}

// validateValidation checks the destruction this project permits, which is a list of operation kinds.
func validateValidation(v *validator, val Validation) {
	for i, kind := range val.AllowDestructive {
		path := fmt.Sprintf("validation.allow_destructive[%d]", i)

		switch {
		case !kind.Valid():
			v.add(path, "%q is not an operation (expected: %s)", string(kind),
				join(ir.DestructiveKinds()))
		case !kind.CanDestroy():
			v.add(path, "%q destroys nothing, so permitting it permits nothing (expected: %s)",
				string(kind), join(ir.DestructiveKinds()))
		case contains(val.AllowDestructive[:i], kind):
			v.add(path, "%q is permitted twice, and once is what permits it", string(kind))
		}
	}

	validateRules(v, val.Rules)
}

// validateRules checks the severity each rule is set to, refusing an id no rule in this build has.
func validateRules(v *validator, rules map[rule.ID]rule.Severity) {
	for _, id := range slices.Sorted(maps.Keys(rules)) {
		path := "validation.rules." + string(id)

		if _, shipped := rule.Default(id); !shipped {
			v.add(path, "no rule is called %q (expected one of: %s)", string(id), rule.JoinIDs())

			continue
		}

		if severity := rules[id]; !severity.Valid() {
			v.add(path, "%s", rule.DescribeSeverity(string(severity)))
		}
	}
}

// validateRewrites checks each rewrite is one this build ships and is set to on or off.
func validateRewrites(v *validator, rewrites map[driver.RewriteName]Switch) {
	for _, name := range slices.Sorted(maps.Keys(rewrites)) {
		path := "safety.rewrites." + string(name)

		if !name.Valid() {
			v.add(path, "no rewrite is called %q (expected one of: %s)", string(name),
				join(driver.RewriteNames()))

			continue
		}

		if value := rewrites[name]; !value.Valid() {
			v.add(path, "%q is not valid (expected: %s or %s)", string(value), SwitchOn, SwitchOff)
		}
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
