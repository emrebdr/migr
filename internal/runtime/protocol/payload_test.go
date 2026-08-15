package protocol

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

func TestEveryDocumentedPayloadIsTheTypeThisBuildCarries(t *testing.T) {
	checked := 0

	for _, m := range readSpec(t).messages {
		decoded, err := Decode([]byte(m.body))
		if err != nil {
			continue
		}

		target, payload := payloadOf(m.method, decoded)
		if target == nil {
			continue
		}

		checked++

		t.Run(m.method, func(t *testing.T) {
			assertPayload(t, m, payload, target)
		})
	}

	if checked == 0 {
		t.Log("The mapping is what makes a documented member and a struct tag answer to each")
		t.Log("other. A mapping that matches nothing is a check that passes by finding no work.")
		t.Fatalf("%s carries no payload this build has a type for", specPath)
	}
}

func TestEveryHostCallbackHasADocumentedPayload(t *testing.T) {
	seen := make(map[string]bool, len(HostMethods()))

	for _, m := range readSpec(t).messages {
		if target, _ := payloadOf(m.method, mustDecode(t, m.body)); target != nil {
			seen[m.method] = true
		}
	}

	for _, method := range HostMethods() {
		if !seen[method] {
			t.Errorf("%s shows no payload for %s that this build has a type for", specPath, method)
			t.Log("A callback whose payload nothing checks is one where the document and the")
			t.Log("struct tags can drift, which is a member an SDK spells one way and we another.")
		}
	}
}

func TestEveryDocumentedPayloadWithMembersHasAType(t *testing.T) {
	for _, m := range readSpec(t).messages {
		decoded, err := Decode([]byte(m.body))
		if err != nil || m.method == "" || decoded.Error != nil {
			continue
		}

		target, payload := payloadOf(m.method, decoded)
		if target != nil || len(bytes.Trim(payload, "{} \t")) == 0 {
			continue
		}

		t.Errorf("%s:%d: %s carries %s and this build has no type for it",
			specPath, m.line, m.method, payload)
		t.Log("An empty payload needs no type. One with members and no type is a payload")
		t.Log("nothing holds to the document, which is where the wire and the code drift.")
	}
}

func TestTheWireSpellsSupportAndLockAsTheCapabilityModelDoes(t *testing.T) {
	for _, support := range capability.Supports() {
		if !slices.Contains(documentedSet(t, "`support` is "), string(support)) {
			t.Errorf("%s never names the support verdict %q", specPath, support)
		}
	}

	for _, impact := range capability.Impacts() {
		if !slices.Contains(documentedSet(t, "`lock` is "), string(impact)) {
			t.Errorf("%s never names the lock impact %q", specPath, impact)
		}
	}
}

func TestTheWireSpellsModeAsTheEngineDoes(t *testing.T) {
	wire := make([]string, 0, len(Modes()))
	for _, mode := range Modes() {
		wire = append(wire, string(mode))
	}

	asked := make([]string, 0, len(engine.Modes()))
	for _, mode := range engine.Modes() {
		asked = append(asked, string(mode))
	}

	if !slices.Equal(wire, asked) {
		t.Errorf("this package spells the modes %q and the engine asks in %q", wire, asked)
		t.Log("The engine owns the distinction -- migr plan over .sql migrations is plan mode")
		t.Log("with no protocol in sight -- and this package owns how it crosses the wire. Two")
		t.Log("spellings are one definition only for as long as something holds them level.")
	}
}

func TestModesAndLevelsAreClosedSets(t *testing.T) {
	if got := Modes(); !slices.Equal(got, []Mode{ModeApply, ModePlan}) {
		t.Errorf("Modes() is %v", got)
	}

	if got := Levels(); !slices.Equal(got, []Level{LevelDebug, LevelInfo, LevelWarn, LevelError}) {
		t.Errorf("Levels() is %v", got)
	}

	for _, unknown := range []string{"", "APPLY", "dry-run", "preview"} {
		if Mode(unknown).Valid() {
			t.Errorf("Mode(%q) is not one of the two §4 defines", unknown)
		}
	}

	for _, unknown := range []string{"", "INFO", "trace", "fatal", "warning"} {
		if Level(unknown).Valid() {
			t.Errorf("Level(%q) is not one of the four §5 defines", unknown)
		}
	}
}

func TestUnmarshalPayloadIgnoresAMemberItDoesNotKnow(t *testing.T) {
	var p DBQueryParams

	if err := UnmarshalPayload([]byte(`{"sql":"SELECT 1","args":[],"future":"member"}`), &p); err != nil {
		t.Fatalf("§10.2 makes an added parameter compatible, so it is ignored rather than refused: %v", err)
	}

	if p.SQL != "SELECT 1" {
		t.Errorf("the statement decoded as %q", p.SQL)
	}
}

func TestAPayloadThatCarriesNothingDecodesAsEmpty(t *testing.T) {
	var p DBQueryParams

	if err := UnmarshalPayload(nil, &p); err != nil {
		t.Fatalf("host.info sends no parameters at all, and that is not a failure: %v", err)
	}
}

func TestRequiredParametersAreCheckedRatherThanAssumed(t *testing.T) {
	if err := (DBQueryParams{}).Validate(); err == nil {
		t.Error("a read with no statement has nothing to run")
	}

	if err := (DBExecParams{}).Validate(); err == nil {
		t.Error("a statement with no text has nothing to run")
	}

	if err := (CapabilityGetParams{}).Validate(); err == nil {
		t.Error("a capability is answered per operation, so a question naming none is not one")
	}

	if err := (LogParams{Level: LevelInfo}).Validate(); err == nil {
		t.Error("a log line with nothing to say is not one")
	}

	if err := (LogParams{Level: "trace", Message: "x"}).Validate(); err == nil {
		t.Error("§5 closes the level set to four, and trace is not among them")
	}

	if err := (LogParams{Level: LevelWarn, Message: "x"}).Validate(); err != nil {
		t.Errorf("a well-formed log line validates: %v", err)
	}
}

func TestACapabilityQuestionCarriesAWholeOperation(t *testing.T) {
	var p CapabilityGetParams

	body := `{"operation":{"op":"drop_column","table":"users","column":"legacy"}}`
	if err := UnmarshalPayload([]byte(body), &p); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if p.Operation == nil || p.Operation.Kind() != ir.KindDropColumn {
		t.Fatalf("the operation decoded as %#v", p.Operation)
	}

	if _, err := MarshalPayload(p); err != nil {
		t.Errorf("re-encoding: %v", err)
	}
}

func TestAnOperationThatIsNotOneIsRefusedRatherThanIgnored(t *testing.T) {
	var p CapabilityGetParams

	if err := UnmarshalPayload([]byte(`{"operation":{"op":"teleport"}}`), &p); err == nil {
		t.Error("an operation this IR version does not define is not a question that can be answered")
	}
}

// payloadOf reports the typed payload one documented message carries, and the bytes to hold it to.
func payloadOf(method string, m *Message) (any, json.RawMessage) {
	if m == nil {
		return nil, nil
	}

	if m.IsResponse() {
		return resultOf(method), m.Result
	}

	return paramsOf(method), m.Params
}

// paramsOf reports the type a callback's parameters decode into, and nothing for one with none.
func paramsOf(method string) any {
	switch method {
	case HostSchemaApply:
		return &SchemaApplyParams{}
	case HostDBQuery:
		return &DBQueryParams{}
	case HostDBExec:
		return &DBExecParams{}
	case HostCapabilityGet:
		return &CapabilityGetParams{}
	case HostLog:
		return &LogParams{}
	case Initialize:
		return &InitializeParams{}
	case MigrationExecute:
		return &ExecuteParams{}
	case MigrationValidate:
		return &ValidateParams{}
	case Cancel:
		return &CancelParams{}
	default:
		return nil
	}
}

// resultOf reports the type a callback's result decodes into, and nothing for one with none.
func resultOf(method string) any {
	switch method {
	case HostSchemaApply:
		return &SchemaApplyResult{}
	case HostDBQuery:
		return &DBQueryResult{}
	case HostDBExec:
		return &DBExecResult{}
	case HostCapabilityGet:
		return &CapabilityGetResult{}
	case HostInfo:
		return &InfoResult{}
	case Initialize:
		return &InitializeResult{}
	case MigrationDiscover:
		return &DiscoverResult{}
	case MigrationExecute:
		return &ExecuteResult{}
	default:
		return nil
	}
}

// assertPayload holds a documented payload to its type, strictly in and byte for byte back out.
func assertPayload(t *testing.T, m message, payload json.RawMessage, target any) {
	t.Helper()

	if err := strictly(payload, target); err != nil {
		t.Errorf("%s:%d: the documented payload does not fit %T: %v", specPath, m.line, target, err)
		t.Log("A member the document states and this build has no tag for is one an SDK sends")
		t.Log("and the host ignores, which is a callback that silently does nothing.")

		return
	}

	encoded, err := MarshalPayload(target)
	if err != nil {
		t.Errorf("%s:%d: %T does not re-encode: %v", specPath, m.line, target, err)

		return
	}

	if !bytes.Equal(encoded, payload) {
		t.Errorf("%s:%d: the payload does not round-trip byte for byte\n  document: %s\n  build:    %s",
			specPath, m.line, payload, encoded)
	}
}

// strictly decodes a payload the way the document is held to it, refusing a member no type declares.
func strictly(payload json.RawMessage, target any) error {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()

	return dec.Decode(target)
}

// documentedSet returns the backticked values a sentence beginning with a phrase lists.
func documentedSet(t *testing.T, phrase string) []string {
	t.Helper()

	_, rest, found := strings.Cut(strings.Join(readSpecLines(t), " "), phrase)
	if !found {
		t.Fatalf("%s no longer states a set beginning %q", specPath, phrase)

		return nil
	}

	sentence, _, _ := strings.Cut(rest, ". ")

	var values []string

	for i, field := range strings.Split(sentence, "`") {
		if i%2 == 1 {
			values = append(values, field)
		}
	}

	return values
}

// mustDecode reads a documented message that every other check has already proved decodes.
func mustDecode(t *testing.T, body string) *Message {
	t.Helper()

	m, err := Decode([]byte(body))
	if err != nil {
		return nil
	}

	return m
}
