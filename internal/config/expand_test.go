package config

import (
	"strings"
	"testing"
)

func TestExpandString(t *testing.T) {
	vars := map[string]string{
		"SET":   "value",
		"EMPTY": "",
	}

	tests := []struct {
		name  string
		in    string
		want  string
		fails bool
	}{
		{name: "no reference", in: "plain", want: "plain"},
		{name: "whole value", in: "${SET}", want: "value"},
		{name: "embedded", in: "prefix-${SET}-suffix", want: "prefix-value-suffix"},
		{name: "two references", in: "${SET}/${SET}", want: "value/value"},
		{name: "set variable beats default", in: "${SET:-fallback}", want: "value"},
		{name: "default used when unset", in: "${MISSING:-fallback}", want: "fallback"},
		{name: "empty default", in: "${MISSING:-}", want: ""},
		{name: "set but empty is not unset", in: "${EMPTY:-fallback}", want: ""},
		{name: "escape yields a literal", in: "$${SET}", want: "${SET}"},
		{name: "bare dollar is literal", in: "pa$$word", want: "pa$$word"},
		{name: "unbraced is literal", in: "$SET", want: "$SET"},
		{name: "unset without default fails", in: "${MISSING}", fails: true},
		{name: "empty name fails", in: "${}", fails: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandString(tt.in, env(vars))

			if tt.fails {
				if err == nil {
					t.Fatalf("expandString(%q) should fail, got %q", tt.in, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("expandString(%q) failed: %v", tt.in, err)
			}

			if got != tt.want {
				t.Errorf("expandString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUnsetVariableErrorNamesPathAndRemedy(t *testing.T) {
	src := []byte(`
version: 1
migrations:
  language: go
databases:
  default:
    driver: postgres
    dsn: ${NEEDED_URL}
`)

	_, err := parse(src, "test.yaml", env(nil))
	if err == nil {
		t.Fatal("an unset variable must be an error")
	}

	msg := err.Error()
	for _, want := range []string{"databases.default.dsn", "NEEDED_URL", "${NEEDED_URL:-value}"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got: %s", want, msg)
		}
	}
}
