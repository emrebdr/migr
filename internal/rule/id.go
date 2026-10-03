package rule

import (
	"fmt"
	"strings"
)

// separator joins the words of a rule's id, and is the only punctuation one may carry.
const separator = "-"

// ID names one rule, and is what migr.yaml configures it by.
type ID string

// String returns the id as written, which is what a report prints and a config file holds.
func (i ID) String() string {
	return string(i)
}

// Valid reports whether an id is kebab-case: lowercase words joined by single hyphens.
func (i ID) Valid() bool {
	return i.check() == nil
}

// check reports what is wrong with an id, in the terms its author can fix.
func (i ID) check() error {
	if i == "" {
		return fmt.Errorf("a rule has no id (expected kebab-case, as in %q)", "missing-primary-key")
	}

	for _, word := range strings.Split(string(i), separator) {
		if word == "" {
			return fmt.Errorf("rule id %q has a leading, trailing, or doubled %s", i, separator)
		}

		if bad, found := firstNotLowerAlnum(word); found {
			return fmt.Errorf("rule id %q is not kebab-case: %q is not a lowercase letter or digit",
				i, bad)
		}
	}

	return nil
}

// firstNotLowerAlnum returns the first character of a word that kebab-case does not allow.
func firstNotLowerAlnum(word string) (string, bool) {
	for _, r := range word {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return string(r), true
		}
	}

	return "", false
}
