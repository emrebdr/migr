package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTheCodecDoesNotEscapeHTMLTheDocumentLeavesAlone(t *testing.T) {
	params, err := MarshalPayload(map[string]string{
		"sql": "SELECT * FROM users WHERE age < 30 AND name = 'a&b'",
	})
	if err != nil {
		t.Fatalf("encoding the payload: %v", err)
	}

	line, err := Encode(NewRequest(1, HostDBQuery, params))
	if err != nil {
		t.Fatalf("encoding the message: %v", err)
	}

	t.Log(`Go's encoder escapes <, > and & to < and friends unless told not to. Two`)
	t.Log("implementations disagreeing about that produce different bytes for one message,")
	t.Log("which is exactly what byte-identical IR across languages may not tolerate.")

	for _, unescaped := range []string{"<", "&"} {
		if !strings.Contains(string(line), unescaped) {
			t.Errorf("the codec escaped %s: %s", unescaped, line)
		}
	}
}

func TestATopLevelArrayIsAProtocolErrorRatherThanABatch(t *testing.T) {
	_, err := Decode([]byte(`[{"jsonrpc":"2.0","id":1,"method":"shutdown"}]`))
	if err == nil {
		t.Fatal("a top-level array decoded as a message")
	}

	code, ok := CodeOf(err)
	if !ok || code != CodeInvalidRequest {
		t.Errorf("a batch was refused with %v, want %d", err, CodeInvalidRequest)
	}

	t.Log("§11: a top-level array is a protocol error, not a batch. Decoded into the envelope")
	t.Log("it fails anyway, with a type error about a struct -- which tells an SDK author")
	t.Log("nothing about why the batch they sent will never be answered.")

	if !strings.Contains(err.Error(), "batch") {
		t.Errorf("a batch was refused without saying so: %v", err)
	}
}

func TestAMessageIsAlwaysWrittenAsExactlyOneLine(t *testing.T) {
	var out strings.Builder

	params, err := json.Marshal(map[string]string{"message": "one\ntwo"})
	if err != nil {
		t.Fatalf("encoding the payload: %v", err)
	}

	if err := NewWriter(&out).Write(NewNotification(HostLog, params)); err != nil {
		t.Fatalf("writing a message whose payload carries a newline: %v", err)
	}

	if strings.Count(strings.TrimRight(out.String(), "\n"), "\n") != 0 {
		t.Errorf("one message was written as more than one line: %q", out.String())
	}
}

func TestALineLongerThanTheBudgetIsRefusedAtIt(t *testing.T) {
	_, err := NewReader(strings.NewReader(strings.Repeat("x", MaxLine+1))).Read()
	if err == nil {
		t.Fatal("a line past the budget was read")
	}

	if code, ok := CodeOf(err); !ok || code != CodeParse {
		t.Errorf("an over-long line was refused with %v, want %d", err, CodeParse)
	}

	t.Log("§11: refused at the budget rather than after a newline that may never arrive, which")
	t.Log("is what stops a peer from making this side buffer without bound.")
}

func TestAByteOrderMarkEndsTheReaderRatherThanBeingSkipped(t *testing.T) {
	marked := "\xef\xbb\xbf" + `{"jsonrpc":"2.0","id":1,"method":"shutdown"}` + "\n"

	_, err := NewReader(strings.NewReader(marked)).Read()
	if err == nil {
		t.Fatal("a line opening with a byte-order mark was read as a message")
	}

	if code, ok := CodeOf(err); !ok || code != CodeParse {
		t.Errorf("a byte-order mark was refused with %v, want %d", err, CodeParse)
	}
}
