package rule

import (
	"strings"
	"testing"
)

func TestAnIdIsKebabCaseOrItIsRefused(t *testing.T) {
	for _, c := range []struct {
		given ID
		valid bool
		says  string
	}{
		{"missing-primary-key", true, ""},
		{"unbounded-string", true, ""},
		{"int32-only", true, ""},
		{"single", true, ""},
		{"", false, "has no id"},
		{"-leading", false, "leading, trailing, or doubled"},
		{"trailing-", false, "leading, trailing, or doubled"},
		{"doubled--hyphen", false, "leading, trailing, or doubled"},
		{"Upper-Case", false, "not kebab-case"},
		{"snake_case", false, "not kebab-case"},
		{"with space", false, "not kebab-case"},
		{"with.dot", false, "not kebab-case"},
	} {
		t.Run(string(c.given), func(t *testing.T) {
			if got := c.given.Valid(); got != c.valid {
				t.Fatalf("Valid() = %v, want %v", got, c.valid)
			}

			err := c.given.check()
			if c.valid {
				if err != nil {
					t.Fatalf("a valid id is refused: %v", err)
				}

				return
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("error %q does not say %q", err, c.says)
			}
		})
	}
}

func TestAnIdRendersAsItIsWritten(t *testing.T) {
	if got := MissingPrimaryKey.String(); got != "missing-primary-key" {
		t.Errorf("String() = %q, want %q", got, "missing-primary-key")
	}
}
