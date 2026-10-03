package rule

import (
	"fmt"
	"slices"
	"strings"
)

// Severity is what a project has decided one rule's findings are worth.
type Severity string

// The three severities a rule can be configured to, which migr.yaml names it by.
const (
	// SeverityError makes a finding a problem, so migr validate exits 2 over it.
	SeverityError Severity = "error"
	// SeverityWarn makes a finding a warning, which is printed and changes no exit code.
	SeverityWarn Severity = "warn"
	// SeverityOff stops the rule from running at all, so it judges nothing and costs nothing.
	SeverityOff Severity = "off"
)

// severities lists every accepted severity, in decreasing order of what it asks of a project.
var severities = []Severity{SeverityError, SeverityWarn, SeverityOff}

// Severities returns every accepted severity, in declaration order, as a copy.
func Severities() []Severity {
	return slices.Clone(severities)
}

// Valid reports whether a value names one of the three severities.
func (s Severity) Valid() bool {
	return slices.Contains(severities, s)
}

// Reports reports whether a rule at this severity produces findings at all.
func (s Severity) Reports() bool {
	return s == SeverityError || s == SeverityWarn
}

// String returns the severity's wire name, which is what migr.yaml holds and --json renders.
func (s Severity) String() string {
	return string(s)
}

// DescribeSeverity renders what a value is not, hinting at the spelling where only case is wrong.
func DescribeSeverity(value string) string {
	if lowered := Severity(strings.ToLower(value)); lowered.Valid() {
		return fmt.Sprintf("%q is not a severity (did you mean %q?)", value, lowered)
	}

	return fmt.Sprintf("%q is not a severity (expected: %s)", value, JoinSeverities())
}

// JoinSeverities renders the severity vocabulary for a message that lists it.
func JoinSeverities() string {
	names := make([]string, len(severities))
	for i, s := range severities {
		names[i] = string(s)
	}

	return strings.Join(names, ", ")
}
