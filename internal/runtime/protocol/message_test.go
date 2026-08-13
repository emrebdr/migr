package protocol

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestEveryShapeRoundTrips(t *testing.T) {
	shapes := []struct {
		name string
		wire string
	}{
		{
			name: "a request",
			wire: `{"jsonrpc":"2.0","id":3,"method":"migration.execute","params":{"id":"20260727T153000","direction":"up","mode":"apply"}}`,
		},
		{
			name: "a notification",
			wire: `{"jsonrpc":"2.0","method":"$/cancel","params":{"id":3}}`,
		},
		{
			name: "a response",
			wire: `{"jsonrpc":"2.0","id":3,"result":{"operations":2}}`,
		},
		{
			name: "a failure",
			wire: `{"jsonrpc":"2.0","id":3,"error":{"code":-32012,"message":"this migration authors no down of its own","data":{"slug":"no_down_authored"}}}`,
		},
		{
			name: "the one message with a null id",
			wire: `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"line 4 is not JSON","data":{"slug":"parse_error"}}}`,
		},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			assertRoundTrip(t, shape.wire)
		})
	}
}

func TestAShapeIsToldApartByWhatItCarries(t *testing.T) {
	request := decoded(t, `{"jsonrpc":"2.0","id":3,"method":"shutdown","params":{}}`)
	notification := decoded(t, `{"jsonrpc":"2.0","method":"host.log","params":{"level":"info","message":"x"}}`)
	response := decoded(t, `{"jsonrpc":"2.0","id":3,"result":{}}`)

	if !request.IsRequest() || request.IsNotification() || request.IsResponse() {
		t.Error("a method and an integer id is a request and nothing else")
	}

	if !notification.IsNotification() || notification.IsRequest() || notification.IsResponse() {
		t.Error("a method and no id is a notification and nothing else")
	}

	if !response.IsResponse() || response.IsRequest() || response.IsNotification() {
		t.Error("no method is a response and nothing else")
	}
}

func TestAnIDIsAbsentAnIntegerOrNull(t *testing.T) {
	absent := ID{}

	if !absent.IsZero() || absent.Null() {
		t.Error("the zero id is absent, which is what makes a call a notification")
	}

	if _, ok := absent.Number(); ok {
		t.Error("an absent id has no number")
	}

	if _, err := absent.MarshalJSON(); err == nil {
		t.Error("an absent id is omitted rather than encoded, so encoding one is a failure")
	}

	numbered := NewID(3)
	if n, ok := numbered.Number(); !ok || n != 3 {
		t.Errorf("NewID(3) reports %d, %v", n, ok)
	}

	null := NullID()
	if !null.Null() || null.IsZero() {
		t.Error("a null id is neither absent nor a number")
	}
}

func TestAnIDOfZeroIsANumberAndNotAnAbsence(t *testing.T) {
	m := decoded(t, `{"jsonrpc":"2.0","id":0,"method":"shutdown","params":{}}`)

	if !m.IsRequest() {
		t.Fatal("id 0 is a number, so this is a request")
	}

	if n, ok := m.ID.Number(); !ok || n != 0 {
		t.Errorf("id 0 decoded as %d, %v", n, ok)
	}

	assertRoundTrip(t, `{"jsonrpc":"2.0","id":0,"method":"shutdown","params":{}}`)
}

func TestDecodeRefusesWhatIsNotAMessage(t *testing.T) {
	for _, c := range refusals() {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(c.wire))
			if err == nil {
				t.Fatalf("Decode accepted %s", c.wire)
			}

			assertCode(t, err, c.code)
		})
	}
}

func TestABatchIsRefusedAsABatchAndNotAsAShape(t *testing.T) {
	_, err := Decode([]byte(`[{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}]`))
	if err == nil {
		t.Fatal("§2.4: an array at the top level of a line is never an implicit batch")
	}

	assertCode(t, err, CodeInvalidRequest)

	if !strings.Contains(err.Error(), "batch") {
		t.Errorf("the failure says %q, and an SDK author who tried to batch needs to be told so", err)
	}
}

func TestAnUnknownMethodDecodesSoThatItCanBeAnswered(t *testing.T) {
	m, err := Decode([]byte(`{"jsonrpc":"2.0","id":7,"method":"host.progress","params":{"done":"1"}}`))
	if err != nil {
		t.Log("§2.6: it is answered -32601 and the session continues, which is what makes")
		t.Log("adding a method a compatible change. Refusing it here ends the session instead.")
		t.Fatalf("a request naming a method this version does not define must decode: %v", err)
	}

	if m.Method != "host.progress" {
		t.Errorf("the method decoded as %q", m.Method)
	}
}

func TestAPayloadIsCarriedVerbatimAndTheEnvelopeIsNot(t *testing.T) {
	withUnknownPayloadMember := `{"jsonrpc":"2.0","id":1,"method":"host.schema.apply","params":{"operations":[{"op":"create_table","table":"users","columns":[]}],"future":"member"}}`
	assertRoundTrip(t, withUnknownPayloadMember)

	_, err := Decode([]byte(`{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{},"future":"member"}`))
	if err == nil {
		t.Log("§10.2 makes an added result or params member compatible and says nothing about")
		t.Log("the envelope, so the envelope is closed and a payload is open.")
		t.Fatal("an unknown member on the envelope is not a message this version defines")
	}

	assertCode(t, err, CodeInvalidRequest)
}

func TestAnErrorWithNoSlugIsRefusedOfASenderAndAcceptedOfAPeer(t *testing.T) {
	m, err := Decode([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32011,"message":"boom"}}`))
	if err != nil {
		t.Fatalf("a peer's sloppy error still reaches the operator: %v", err)
	}

	if m.Error.Slug() != "" {
		t.Errorf("the error carries the slug %q and it carried none", m.Error.Slug())
	}

	if err := m.Error.Validate(); err == nil {
		t.Error("a sender owes §7.1 a slug, so validating one without it must fail")
	}

	if err := NewError(CodeMigrationFailed, "boom").Validate(); err != nil {
		t.Errorf("NewError fills the slug from the code itself: %v", err)
	}
}

func TestEveryCodeNamesItselfInTheErrorItBuilds(t *testing.T) {
	for _, code := range assigned() {
		e := NewError(code, "something happened")

		if e.Slug() != code.String() {
			t.Errorf("NewError(%d) carries the slug %q, and the code names itself %q",
				code, e.Slug(), code)
		}
	}
}

func TestOnlyCancelAndLogMayBeNotifications(t *testing.T) {
	want := []string{Cancel, HostLog}

	if got := Notifications(); !slices.Equal(got, want) {
		t.Errorf("Notifications() is %v, want %v", got, want)
	}

	for _, method := range Methods() {
		notifies := Notifies(method)

		if notifies != slices.Contains(want, method) {
			t.Errorf("Notifies(%q) is %v", method, notifies)
		}
	}
}

// refusal is one line the decoder must refuse, and the code §2.6 says it must refuse it with.
type refusal struct {
	name string
	wire string
	code Code
}

// refusals lists every line that is not a message, read as §2.6's table reads them.
func refusals() []refusal {
	return []refusal{
		{"nothing", ``, CodeParse},
		{"not JSON at all", `xyzzy`, CodeParse},
		{"JSON that stops early", `{"jsonrpc":"2.0","id":1`, CodeParse},
		{"a batch", `[{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}}]`, CodeInvalidRequest},
		{"a number", `123`, CodeInvalidRequest},
		{"a string", `"shutdown"`, CodeInvalidRequest},
		{"no jsonrpc", `{"id":1,"method":"shutdown","params":{}}`, CodeInvalidRequest},
		{"jsonrpc 1.0", `{"jsonrpc":"1.0","id":1,"method":"shutdown","params":{}}`, CodeInvalidRequest},
		{"a string id", `{"jsonrpc":"2.0","id":"1","method":"shutdown","params":{}}`, CodeInvalidRequest},
		{"a fractional id", `{"jsonrpc":"2.0","id":3.5,"method":"shutdown","params":{}}`, CodeInvalidRequest},
		{"an exponent id", `{"jsonrpc":"2.0","id":1e3,"method":"shutdown","params":{}}`, CodeInvalidRequest},
		{"a boolean id", `{"jsonrpc":"2.0","id":true,"method":"shutdown","params":{}}`, CodeInvalidRequest},
		{"a null id on a request", `{"jsonrpc":"2.0","id":null,"method":"shutdown","params":{}}`, CodeInvalidRequest},
		{"a notification for a method that is not one", `{"jsonrpc":"2.0","method":"migration.execute","params":{}}`, CodeInvalidRequest},
		{"a notification for a method nothing defines", `{"jsonrpc":"2.0","method":"host.progress","params":{}}`, CodeInvalidRequest},
		{"a result and an error", `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32011,"message":"x","data":{"slug":"migration_failed"}}}`, CodeInvalidRequest},
		{"neither a result nor an error", `{"jsonrpc":"2.0","id":1}`, CodeInvalidRequest},
		{"a null id carrying a result", `{"jsonrpc":"2.0","id":null,"result":{}}`, CodeInvalidRequest},
		{"a method beside a result", `{"jsonrpc":"2.0","id":1,"method":"shutdown","result":{}}`, CodeInvalidRequest},
		{"an unknown envelope member", `{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{},"extra":1}`, CodeInvalidRequest},
		{"two messages in one line", `{"jsonrpc":"2.0","id":1,"method":"shutdown","params":{}} {"jsonrpc":"2.0","id":2,"method":"shutdown","params":{}}`, CodeInvalidRequest},
	}
}

// assertRoundTrip reports whether a line decodes and re-encodes to the bytes it arrived as.
func assertRoundTrip(t *testing.T, wire string) {
	t.Helper()

	m := decoded(t, wire)

	encoded, err := Encode(m)
	if err != nil {
		t.Fatalf("Encode(%s): %v", wire, err)
	}

	if string(encoded) != wire {
		t.Errorf("the message does not round-trip\n  in:  %s\n  out: %s", wire, encoded)
	}
}

// decoded reads one line that must be a message, failing the test when it is not.
func decoded(t *testing.T, wire string) *Message {
	t.Helper()

	m, err := Decode([]byte(wire))
	if err != nil {
		t.Fatalf("Decode(%s): %v", wire, err)
	}

	return m
}

// assertCode reports whether a failure carries the JSON-RPC code the specification assigns it.
func assertCode(t *testing.T, err error, want Code) {
	t.Helper()

	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("the failure is %T, and every framing failure is a *protocol.Error: %v", err, err)
	}

	if e.Code != want {
		t.Errorf("the failure carries code %d (%s), want %d (%s): %v", e.Code, e.Code, want, want, err)
	}

	if e.Slug() == "" {
		t.Errorf("the failure carries no data.slug, which §7.1 requires: %v", err)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Errorf("data is not an object: %v", err)
	}
}
