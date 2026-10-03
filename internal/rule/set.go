package rule

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Set is the rules one project runs, each at the severity that project gave it.
type Set struct {
	// rules are the rules themselves, sorted by id, which is the order judgements come back in.
	rules []Rule
	// severity is each rule's resolved severity, which is its default unless the project said.
	severity map[ID]Severity
}

// NewSet resolves every rule's severity against what a project configured, refusing what cannot run.
func NewSet(rules []Rule, configured map[ID]Severity) (Set, error) {
	if err := checkRules(rules); err != nil {
		return Set{}, err
	}

	if err := checkConfigured(rules, configured); err != nil {
		return Set{}, err
	}

	sorted := slices.Clone(rules)
	slices.SortStableFunc(sorted, func(a, b Rule) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})

	resolved := make(map[ID]Severity, len(sorted))
	for _, r := range sorted {
		resolved[r.ID] = severityOf(r, configured)
	}

	return Set{rules: sorted, severity: resolved}, nil
}

// severityOf returns what a project decided one rule is worth, which is its default unless it said.
func severityOf(r Rule, configured map[ID]Severity) Severity {
	if given, said := configured[r.ID]; said {
		return given
	}

	return r.Default
}

// checkRules reports a rule that cannot be run or cannot be configured.
func checkRules(rules []Rule) error {
	seen := make(map[ID]bool, len(rules))

	for _, r := range rules {
		if err := r.ID.check(); err != nil {
			return err
		}

		if seen[r.ID] {
			return fmt.Errorf("rule %q is declared twice, and one declaration is what runs it", r.ID)
		}

		seen[r.ID] = true

		if err := checkRule(r); err != nil {
			return err
		}
	}

	return nil
}

// checkRule reports what one rule is missing, each of which makes it unrunnable or unreadable.
func checkRule(r Rule) error {
	if r.Judge == nil {
		return fmt.Errorf("rule %q judges nothing, so it can report nothing", r.ID)
	}

	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("rule %q has no summary, so migr config cannot say what it checks", r.ID)
	}

	if !r.Default.Valid() {
		return fmt.Errorf("rule %q defaults to %s", r.ID, DescribeSeverity(string(r.Default)))
	}

	return nil
}

// checkConfigured reports a severity nothing recognizes, and an id no rule answers to.
func checkConfigured(rules []Rule, configured map[ID]Severity) error {
	for _, id := range slices.Sorted(maps.Keys(configured)) {
		if !slices.ContainsFunc(rules, func(r Rule) bool { return r.ID == id }) {
			return fmt.Errorf("no rule is called %q (expected one of: %s)", id, joinIDs(rules))
		}

		if given := configured[id]; !given.Valid() {
			return fmt.Errorf("rule %q is set to %s", id, DescribeSeverity(string(given)))
		}
	}

	return nil
}

// Judge reports everything every enabled rule found, in rule order, stamped with its severity.
func (s Set) Judge(subject Subject) []Judgement {
	var found []Judgement

	for _, r := range s.rules {
		severity := s.severity[r.ID]
		if !severity.Reports() {
			continue
		}

		for _, finding := range r.Judge(subject) {
			found = append(found, Judgement{Rule: r.ID, Severity: severity, Finding: finding})
		}
	}

	return found
}

// Severity returns what this project decided one rule is worth, and off for a rule it has none of.
func (s Set) Severity(id ID) Severity {
	if given, held := s.severity[id]; held {
		return given
	}

	return SeverityOff
}

// IDs returns every rule in the set, sorted, which is the order it judges in.
func (s Set) IDs() []ID {
	ids := make([]ID, 0, len(s.rules))
	for _, r := range s.rules {
		ids = append(ids, r.ID)
	}

	return ids
}

// Zero reports whether the set was never built, which a set built over no rules at all is not.
func (s Set) Zero() bool {
	return s.severity == nil
}

// joinIDs renders a rule list's ids for a message that lists them, sorted.
func joinIDs(rules []Rule) string {
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		names = append(names, string(r.ID))
	}

	slices.Sort(names)

	return strings.Join(names, ", ")
}
