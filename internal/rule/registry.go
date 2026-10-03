package rule

import (
	"slices"
	"strings"
)

// builtIn lists every rule this build ships, which is what a project configures and nothing else.
func builtIn() []Rule {
	return []Rule{
		missingPrimaryKey(),
		unboundedString(),
	}
}

// All returns every rule this build ships, sorted by id, as a copy.
func All() []Rule {
	rules := builtIn()
	slices.SortStableFunc(rules, func(a, b Rule) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})

	return rules
}

// IDs returns the id of every rule this build ships, sorted, which is what migr.yaml may name.
func IDs() []ID {
	rules := All()

	ids := make([]ID, 0, len(rules))
	for _, r := range rules {
		ids = append(ids, r.ID)
	}

	return ids
}

// Default returns the severity a project that says nothing about one rule gets for it.
func Default(id ID) (Severity, bool) {
	for _, r := range builtIn() {
		if r.ID == id {
			return r.Default, true
		}
	}

	return "", false
}

// Defaults returns every rule's default severity, which is what an unconfigured project resolves to.
func Defaults() map[ID]Severity {
	rules := builtIn()

	defaults := make(map[ID]Severity, len(rules))
	for _, r := range rules {
		defaults[r.ID] = r.Default
	}

	return defaults
}

// Summary returns what one rule checks, in a line, and nothing for an id no rule answers to.
func Summary(id ID) string {
	for _, r := range builtIn() {
		if r.ID == id {
			return r.Summary
		}
	}

	return ""
}

// JoinIDs renders every shipped rule's id for a message that lists them.
func JoinIDs() string {
	ids := IDs()

	names := make([]string, 0, len(ids))
	for _, id := range ids {
		names = append(names, string(id))
	}

	return strings.Join(names, ", ")
}
