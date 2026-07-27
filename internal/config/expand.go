package config

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"
)

// expandEnv substitutes ${VAR} references in every string scalar of the tree.
func expandEnv(file *ast.File, lookup func(string) (string, bool)) error {
	v := &expander{lookup: lookup}

	for _, doc := range file.Docs {
		ast.Walk(v, doc.Body)
	}

	return v.err
}

// expander is an ast.Visitor that rewrites string scalars in place.
type expander struct {
	lookup func(string) (string, bool)
	err    error
}

// Visit expands a string node, recording the first failure and stopping.
func (e *expander) Visit(node ast.Node) ast.Visitor {
	if e.err != nil {
		return nil
	}

	s, ok := node.(*ast.StringNode)
	if !ok {
		return e
	}

	expanded, err := expandString(s.Value, e.lookup)
	if err != nil {
		e.err = fmt.Errorf("%s: %w", yamlPath(node), err)

		return nil
	}

	s.Value = expanded

	return e
}

// expandString resolves ${VAR} and ${VAR:-default} references in one value.
func expandString(s string, lookup func(string) (string, bool)) (string, error) {
	var b strings.Builder

	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])

			continue
		}

		if literal, width, ok := literalDollar(s[i:]); ok {
			b.WriteString(literal)
			i += width - 1

			continue
		}

		ref, width, ok := reference(s[i:])
		if !ok {
			b.WriteByte(s[i])

			continue
		}

		value, err := resolve(ref, lookup)
		if err != nil {
			return "", err
		}

		b.WriteString(value)
		i += width - 1
	}

	return b.String(), nil
}

// literalDollar reports whether s begins with the $${...} escape sequence.
func literalDollar(s string) (literal string, width int, ok bool) {
	if !strings.HasPrefix(s, "$${") {
		return "", 0, false
	}

	end := strings.IndexByte(s, '}')
	if end < 0 {
		return "", 0, false
	}

	return s[1 : end+1], end + 1, true
}

// reference extracts the body of a leading ${...} expression.
func reference(s string) (body string, width int, ok bool) {
	if !strings.HasPrefix(s, "${") {
		return "", 0, false
	}

	end := strings.IndexByte(s, '}')
	if end < 0 {
		return "", 0, false
	}

	return s[2:end], end + 1, true
}

// resolve looks up a reference body, applying its :- default when unset.
func resolve(ref string, lookup func(string) (string, bool)) (string, error) {
	name, fallback, hasFallback := strings.Cut(ref, ":-")

	if name == "" {
		return "", fmt.Errorf("malformed reference %q: variable name is empty", "${"+ref+"}")
	}

	if value, ok := lookup(name); ok {
		return value, nil
	}

	if hasFallback {
		return fallback, nil
	}

	return "", fmt.Errorf("environment variable %s is not set and has no default (use ${%s:-value})", name, name)
}
