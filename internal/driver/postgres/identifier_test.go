package postgres

import (
	"strconv"
	"strings"
	"testing"
)

func TestEveryIdentifierIsQuotedWhetherItNeedsToBeOrNot(t *testing.T) {
	tests := []struct {
		name string
		want string
		why  string
	}{
		{name: "users", want: `"users"`, why: "an ordinary name is quoted too"},
		{name: "order", want: `"order"`, why: "a reserved word is only a keyword unquoted"},
		{name: "userID", want: `"userID"`, why: "an unquoted mixed-case name folds to lower case"},
		{name: "USERS", want: `"USERS"`, why: "so does an upper-case one"},
		{name: `we"ird`, want: `"we""ird"`, why: "an embedded quote is doubled, never stripped"},
		{name: `"`, want: `""""`, why: "a name that is only a quote still closes"},
		{name: "a b", want: `"a b"`, why: "a space is a name, not a separator"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quote(tt.name); got != tt.want {
				t.Errorf("quote(%q) = %s, want %s -- %s", tt.name, got, tt.want, tt.why)
			}
		})
	}

	t.Log("Quoting unconditionally is what handles reserved words and mixed case with one rule.")
	t.Log("A reserved-word list would need updating every PostgreSQL release and would be")
	t.Log("silently wrong in between.")
}

func TestAColumnListKeepsTheOrderItWasDeclaredIn(t *testing.T) {
	columns := []string{"last", "first", "middle"}

	if got := quoteAll(columns); got != `"last", "first", "middle"` {
		t.Errorf("quoteAll = %s, want the declared order", got)
		t.Log("Index and key column order is significant, so sorting or ranging a map here would")
		t.Log("build a different index from the one the migration declared (invariant #8).")
	}
}

func TestAnIdentifierPostgresWouldTruncateIsRefused(t *testing.T) {
	longest := strings.Repeat("a", maxIdentifier)

	if problem := checkIdentifier("table", longest); problem != "" {
		t.Errorf("a %d-byte name was refused: %s", maxIdentifier, problem)
	}

	tooLong := longest + "a"

	problem := checkIdentifier("table", tooLong)
	if problem == "" {
		t.Log("Truncation is a NOTICE rather than an error, so two distinct long names become")
		t.Log("one object and the second statement silently addresses the first one's.")
		t.Fatalf("a %d-byte name was accepted, and PostgreSQL would truncate it", len(tooLong))
	}

	if !strings.Contains(problem, strconv.Itoa(len(tooLong))) {
		t.Errorf("the refusal %q does not say how long the name actually is", problem)
	}
}

func TestTheLengthLimitIsCountedInBytesRatherThanRunes(t *testing.T) {
	name := strings.Repeat("é", 32)

	if len([]rune(name)) > maxIdentifier {
		t.Fatalf("the fixture is %d runes, which is over the limit on its own", len([]rune(name)))
	}

	if checkIdentifier("table", name) == "" {
		t.Errorf("a %d-byte, %d-rune name was accepted", len(name), len([]rune(name)))
		t.Log("NAMEDATALEN is a byte count, so a name that fits in runes can still be truncated.")
		t.Log("Counting runes would accept exactly the names PostgreSQL silently shortens.")
	}
}

func TestAnIdentifierHoldingANulByteIsRefused(t *testing.T) {
	if checkIdentifier("table", "users\x00 DROP TABLE accounts") == "" {
		t.Error("a name holding a NUL byte was accepted")
		t.Log("A query reaches the server as a C string, so everything after the NUL is dropped:")
		t.Log("the statement that runs is not the statement that was compiled.")
	}
}
