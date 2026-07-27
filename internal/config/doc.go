// Package config loads and validates migr.yaml: migration source settings,
// database definitions with ${VAR} environment expansion, history and lock
// configuration, and the safety thresholds that gate zero-downtime checks.
//
// Unknown keys are an error rather than a silent no-op, and missing required
// fields are reported with the offending YAML path.
//
// Status: scaffolding. Implemented in M0-T7 (see TASKS.md).
package config
