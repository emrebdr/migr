package sql

import "strings"

// lineComment opens a comment that runs to the end of its line.
const lineComment = "--"

// blockOpen and blockClose bracket a comment that may nest, as PostgreSQL's do.
const (
	blockOpen  = "/*"
	blockClose = "*/"
)

// Statements splits one section into the statements it holds, on the semicolons no quoting hides.
func Statements(section string) []string {
	s := &scanner{src: section}
	s.run()

	return s.out
}

// scanner walks a section one byte at a time, tracking what quoting or comment it is inside.
type scanner struct {
	// src is the section being split.
	src string
	// start is where the statement being accumulated began.
	start int
	// pos is how far the scan has read.
	pos int
	// code reports whether anything outside a comment has been seen since the last semicolon.
	code bool
	// out is the statements found so far, in the order they appear.
	out []string
}

// run reads to the end, ending a statement at every semicolon nothing is quoting.
func (s *scanner) run() {
	for s.pos < len(s.src) {
		if s.skipped() {
			continue
		}

		if s.src[s.pos] == ';' {
			s.pos++
			s.emit()

			continue
		}

		s.advance()
	}

	s.emit()
}

// skipped consumes a comment or a quoted run whole, reporting whether it consumed anything.
func (s *scanner) skipped() bool {
	return s.skipLineComment() || s.skipBlockComment() ||
		s.skipQuoted('\'') || s.skipQuoted('"') || s.skipDollarQuoted()
}

// advance consumes one ordinary byte, noting whether this statement has anything to run.
func (s *scanner) advance() {
	if !isSpace(s.src[s.pos]) {
		s.code = true
	}

	s.pos++
}

// emit ends the statement being accumulated, dropping one that is only whitespace and comments.
func (s *scanner) emit() {
	statement := strings.TrimSpace(s.src[s.start:s.pos])

	if s.code && statement != "" {
		s.out = append(s.out, statement)
	}

	s.start = s.pos
	s.code = false
}

// skipLineComment consumes a -- comment through to the end of its line, but not the newline itself.
func (s *scanner) skipLineComment() bool {
	if !s.at(lineComment) {
		return false
	}

	if end := strings.IndexByte(s.src[s.pos:], '\n'); end >= 0 {
		s.pos += end

		return true
	}

	s.pos = len(s.src)

	return true
}

// skipBlockComment consumes a block comment whole, counting the nesting PostgreSQL allows.
func (s *scanner) skipBlockComment() bool {
	if !s.at(blockOpen) {
		return false
	}

	depth := 0

	for s.pos < len(s.src) {
		switch {
		case s.at(blockOpen):
			depth++
			s.pos += len(blockOpen)
		case s.at(blockClose):
			depth--
			s.pos += len(blockClose)
		default:
			s.pos++
		}

		if depth == 0 {
			return true
		}
	}

	return true
}

// skipQuoted consumes a run bracketed by one quote character, where the quote doubled is an escape.
func (s *scanner) skipQuoted(quote byte) bool {
	if s.src[s.pos] != quote {
		return false
	}

	s.code = true
	s.pos++

	for s.pos < len(s.src) {
		if s.src[s.pos] != quote {
			s.pos++

			continue
		}

		s.pos++

		if s.pos < len(s.src) && s.src[s.pos] == quote {
			s.pos++

			continue
		}

		return true
	}

	return true
}

// skipDollarQuoted consumes a $tag$ ... $tag$ body, which is where a function's semicolons live.
func (s *scanner) skipDollarQuoted() bool {
	tag, ok := dollarTag(s.src[s.pos:])
	if !ok {
		return false
	}

	s.code = true
	s.pos += len(tag)

	end := strings.Index(s.src[s.pos:], tag)
	if end < 0 {
		s.pos = len(s.src)

		return true
	}

	s.pos += end + len(tag)

	return true
}

// dollarTag reads the $$ or $name$ delimiter a dollar-quoted body opens with.
func dollarTag(src string) (string, bool) {
	if len(src) < 2 || src[0] != '$' {
		return "", false
	}

	for i := 1; i < len(src); i++ {
		if src[i] == '$' {
			return src[:i+1], true
		}

		if !tagByte(src[i], i == 1) {
			return "", false
		}
	}

	return "", false
}

// tagByte reports whether a byte may appear in a dollar-quote tag, which may not open with a digit.
func tagByte(b byte, first bool) bool {
	if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' {
		return true
	}

	return !first && b >= '0' && b <= '9'
}

// at reports whether the scan sits on the given token.
func (s *scanner) at(token string) bool {
	return strings.HasPrefix(s.src[s.pos:], token)
}

// isSpace reports the whitespace SQL may separate tokens with.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}
