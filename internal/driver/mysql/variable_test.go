package mysql

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOneModeIsAddedToWhateverElseTheOperatorSet(t *testing.T) {
	for name, c := range map[string]struct {
		current string
		want    string
	}{
		"an ordinary set": {
			current: "STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION",
			want:    "STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION," + noBackslashEscapes,
		},
		"an empty set": {current: "", want: noBackslashEscapes},
		"a set that already has it": {
			current: "STRICT_TRANS_TABLES," + noBackslashEscapes,
			want:    "STRICT_TRANS_TABLES," + noBackslashEscapes,
		},
		"a set where it is first": {
			current: noBackslashEscapes + ",STRICT_TRANS_TABLES",
			want:    noBackslashEscapes + ",STRICT_TRANS_TABLES",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := withMode(c.current, noBackslashEscapes); got != c.want {
				t.Errorf("withMode(%q) returned %q, want %q", c.current, got, c.want)
			}
		})
	}

	t.Log("Adding rather than replacing, because sql_mode carries the operator's own strictness")
	t.Log("settings and this driver owns exactly one of its modes.")
}

func TestModeMembershipIsCheckedRatherThanTheWholeStringCompared(t *testing.T) {
	if !hasMode("ONLY_FULL_GROUP_BY,"+noBackslashEscapes+",STRICT_TRANS_TABLES",
		noBackslashEscapes) {
		t.Error("a mode the server moved to the middle is not found")
	}

	if hasMode("NO_BACKSLASH_ESCAPES_X", noBackslashEscapes) {
		t.Error("a longer mode name matches as this one")
	}

	if hasMode("", noBackslashEscapes) {
		t.Error("an empty sql_mode carries a mode")
	}

	t.Log("The server reorders sql_mode when it takes it -- measured: adding NO_BACKSLASH_ESCAPES to")
	t.Log("MySQL's default puts it second and to MariaDB's puts it first -- so reading it back means")
	t.Log("asking whether the mode is in the list, never whether the string is the one that was sent.")
}

func TestAWaitIsRenderedAsTheWholeSecondsBothTimeoutsTake(t *testing.T) {
	for wait, want := range map[time.Duration]string{
		time.Second:      "1",
		3 * time.Second:  "3",
		90 * time.Second: "90",
	} {
		if got := waitSeconds(wait); got != want {
			t.Errorf("waitSeconds(%s) returned %q, want %q", wait, got, want)
		}
	}
}

func TestAValueWrittenIntoAStatementMustBeANumber(t *testing.T) {
	for _, value := range []string{"3", "0", "31536000"} {
		if err := checkDigits(varLockWait, value); err != nil {
			t.Errorf("checkDigits(%q): %v", value, err)
		}
	}

	for _, value := range []string{"", "3; DROP TABLE users", "-1", "3.5", "0x10", " 3"} {
		if err := checkDigits(varLockWait, value); !errors.Is(err, ErrConnect) {
			t.Errorf("checkDigits(%q) returned %v, want a refusal", value, err)
		}
	}

	t.Log("A numeric or boolean system variable takes no bound parameter -- error 1232 for")
	t.Log("lock_wait_timeout and 1231 for transaction_read_only, measured on both servers -- so the")
	t.Log("digits are written into the statement, and this is what keeps that structurally safe")
	t.Log("rather than safe because today's callers happen to pass constants.")
}

func TestTheCharacterSetRefusalNamesWhatToPutInTheConnectionString(t *testing.T) {
	err := (&CharacterSetError{Got: "latin1", Want: requiredCharacterSet}).Error()

	for _, part := range []string{"latin1", "utf8mb4", "charset="} {
		if !strings.Contains(err, part) {
			t.Errorf("the refusal does not name %q: %s", part, err)
		}
	}

	t.Log("Checked rather than set, because a character set is the operator's statement about their")
	t.Log("server -- and every value crosses the wire as a JSON string, so a connection that")
	t.Log("transcodes hands a migration characters that are not in the database.")
}

func TestASessionVariableThatReadsBackWrongSaysWhatItWasSetTo(t *testing.T) {
	err := (&VariableError{Variable: varLockWait, Want: "0", Got: "1"}).Error()

	for _, part := range []string{varLockWait, "0", "1", "clamps"} {
		if !strings.Contains(err, part) {
			t.Errorf("the message does not carry %q: %s", part, err)
		}
	}

	if !errors.Is(&VariableError{}, ErrVariable) {
		t.Error("a VariableError is not ErrVariable")
	}
}
