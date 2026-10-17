package sql

import (
	"fmt"
	"strings"
)

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

// statement is one statement a section holds, and whether a marker declared it runs outside a transaction.
type statement struct {
	// sql is the statement as written, comments above it included.
	sql string
	// standalone reports a statement the no-transaction marker sits directly above.
	standalone bool
}

// misplaced is a no-transaction marker the scanner refused, at the line it is on within its section.
type misplaced struct {
	// line counts from one, within the section.
	line int
	// why says what about its place is wrong.
	why string
}

// scan splits a section into statements, noting which a no-transaction marker declared, or refuses one.
func scan(section string) ([]statement, *misplaced) {
	s := &scanner{src: section, marking: true}
	s.run()

	if s.problem != nil {
		return nil, s.problem
	}

	out := make([]statement, len(s.out))
	for i, sql := range s.out {
		if s.standalone[i] {
			sql = unmarked(sql)
		}

		out[i] = statement{sql: sql, standalone: s.standalone[i]}
	}

	return out, nil
}

// unmarked removes the marker line from the statement it declared, since it is migr's and not the server's.
func unmarked(sql string) string {
	lines := strings.Split(sql, "\n")

	for i, line := range lines {
		if strings.TrimSpace(line) == MarkerNoTransaction {
			return strings.TrimSpace(strings.Join(append(lines[:i:i], lines[i+1:]...), "\n"))
		}
	}

	return sql
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
	// marking reports a scan that reads the no-transaction marker rather than passing it over.
	marking bool
	// pending is the line of a marker waiting for the statement below it, zero when none is.
	pending int
	// standalone holds, per statement in out, whether a marker declared it.
	standalone []bool
	// problem is the first marker refused, which ends what the scan reports.
	problem *misplaced
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

	if s.pending > 0 {
		s.refuse(s.pending, "nothing follows it in its section, so it declares no statement")
	}
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
		s.standalone = append(s.standalone, s.pending > 0)
		s.pending = 0
	}

	s.start = s.pos
	s.code = false
}

// skipLineComment consumes a -- comment through to the end of its line, but not the newline itself.
func (s *scanner) skipLineComment() bool {
	if !s.at(lineComment) {
		return false
	}

	end := len(s.src)
	if newline := strings.IndexByte(s.src[s.pos:], '\n'); newline >= 0 {
		end = s.pos + newline
	}

	if s.marking && s.aloneOnItsLine() && strings.TrimSpace(s.src[s.pos:end]) == MarkerNoTransaction {
		s.mark()
	}

	s.pos = end

	return true
}

// aloneOnItsLine reports a comment with nothing but whitespace before it on its line.
func (s *scanner) aloneOnItsLine() bool {
	lineStart := strings.LastIndexByte(s.src[:s.pos], '\n') + 1

	return strings.TrimSpace(s.src[lineStart:s.pos]) == ""
}

// mark notes a no-transaction marker for the statement below it, refusing one with no place to be.
func (s *scanner) mark() {
	line := s.lineAt(s.pos)

	switch {
	case s.code:
		s.refuse(line, "it sits inside a statement, which no semicolon has ended above it, "+
			"and it declares the statement below it rather than one it is part of")
	case s.pending > 0:
		s.refuse(line, fmt.Sprintf("the marker on line %d is waiting for its statement too, and "+
			"one marker declares one statement", s.pending))
	default:
		s.pending = line
	}
}

// refuse records the first marker the scan cannot accept.
func (s *scanner) refuse(line int, why string) {
	if s.problem == nil {
		s.problem = &misplaced{line: line, why: why}
	}
}

// lineAt counts which line of the section a position is on, from one.
func (s *scanner) lineAt(pos int) int {
	return strings.Count(s.src[:pos], "\n") + 1
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
