package ir

import (
	"bytes"
	"strings"
	"testing"
)

func TestRejectsMalformedDocuments(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "no discriminator",
			json: `{"table":"users"}`,
			want: `has no "op" field`,
		},
		{
			name: "unknown operation",
			json: `{"op":"truncate_table","table":"users"}`,
			want: `unknown operation "truncate_table"`,
		},
		{
			name: "unknown field",
			json: `{"op":"drop_table","table":"users","cascade":true}`,
			want: `unknown field "cascade"`,
		},
		{
			name: "missing required field",
			json: `{"op":"drop_table"}`,
			want: "table is required",
		},
		{
			name: "create_table with no columns",
			json: `{"op":"create_table","table":"users","columns":[]}`,
			want: "columns must list at least one column",
		},
		{
			name: "create_table with a duplicate column",
			json: `{"op":"create_table","table":"users","columns":[` +
				`{"name":"id","type":"uuid","nullable":false},` +
				`{"name":"id","type":"uuid","nullable":false}]}`,
			want: `column "id" is declared more than once`,
		},
		{
			name: "primary key naming an undeclared column",
			json: `{"op":"create_table","table":"users","columns":[` +
				`{"name":"id","type":"uuid","nullable":false}],"primary_key":["missing"]}`,
			want: `"missing" is not a column of this table`,
		},
		{
			name: "column with no type",
			json: `{"op":"add_column","table":"users","column":{"name":"x","nullable":false}}`,
			want: `column "x" has no type`,
		},
		{
			name: "column with a malformed type",
			json: `{"op":"add_column","table":"users","column":{"name":"x","type":"decimal(10)","nullable":false}}`,
			want: "a precision and a scale",
		},
		{
			name: "rename to the same name",
			json: `{"op":"rename_table","from":"users","to":"users"}`,
			want: "same from and to",
		},
		{
			name: "alter_column requesting nothing",
			json: `{"op":"alter_column","table":"users","column":"email"}`,
			want: "requests no change",
		},
		{
			name: "alter_column setting and dropping a default",
			json: `{"op":"alter_column","table":"users","column":"email",` +
				`"default":{"literal":"x"},"drop_default":true}`,
			want: "cannot both be set",
		},
		{
			name: "default with both literal and function",
			json: `{"op":"add_column","table":"users","column":{"name":"c","type":"text",` +
				`"nullable":false,"default":{"literal":"x","function":"uuid"}}}`,
			want: "not both",
		},
		{
			name: "unknown default function",
			json: `{"op":"add_column","table":"users","column":{"name":"c","type":"text",` +
				`"nullable":false,"default":{"function":"now"}}}`,
			want: `unknown default function "now"`,
		},
		{
			name: "unknown referential action",
			json: `{"op":"add_foreign_key","table":"orders","name":"fk","columns":["a"],` +
				`"references_table":"users","references_columns":["id"],"on_delete":"nullify"}`,
			want: `unknown on_delete action "nullify"`,
		},
		{
			name: "foreign key with mismatched widths",
			json: `{"op":"add_foreign_key","table":"orders","name":"fk","columns":["a","b"],` +
				`"references_table":"users","references_columns":["id"]}`,
			want: "columns has 2 entries but references_columns has 1",
		},
		{
			name: "index with no columns",
			json: `{"op":"add_index","table":"users","name":"idx","columns":[]}`,
			want: "columns must list at least one column",
		},
		{
			name: "index repeating a column",
			json: `{"op":"add_index","table":"users","name":"idx","columns":["a","a"]}`,
			want: `columns lists "a" more than once`,
		},
		{
			name: "check with no expression",
			json: `{"op":"add_check","table":"orders","name":"ck","expression":""}`,
			want: "expression is required",
		},
		{
			name: "view with no query",
			json: `{"op":"create_view","view":"v","query":""}`,
			want: "query is required",
		},
		{
			name: "raw without a driver",
			json: `{"op":"raw","sql":"SELECT 1"}`,
			want: "driver is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op, err := UnmarshalOperation([]byte(tt.json))
			if err == nil {
				t.Fatalf("should have been rejected, decoded %#v", op)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}

			if op != nil {
				t.Errorf("a rejected document must decode to nil, got %#v", op)
			}
		})
	}
}

func TestEveryRejectionIsSpecific(t *testing.T) {
	seen := make(map[string]string)

	for _, tt := range []struct{ name, json string }{
		{"no discriminator", `{"table":"users"}`},
		{"unknown op", `{"op":"truncate_table"}`},
		{"unknown field", `{"op":"drop_table","table":"u","cascade":true}`},
		{"missing field", `{"op":"drop_table"}`},
		{"no change", `{"op":"alter_column","table":"u","column":"c"}`},
		{"bad function", `{"op":"add_column","table":"u","column":{"name":"c","type":"text","nullable":false,"default":{"function":"now"}}}`},
	} {
		_, err := UnmarshalOperation([]byte(tt.json))
		if err == nil {
			t.Fatalf("%s: should have been rejected", tt.name)
		}

		if prior, ok := seen[err.Error()]; ok {
			t.Errorf("%s and %s share the message %q; each failure needs its own diagnosis",
				prior, tt.name, err)
		}

		seen[err.Error()] = tt.name
	}
}

func TestDestructivenessIsTotal(t *testing.T) {
	want := map[Kind]bool{
		KindCreateTable:    false,
		KindDropTable:      true,
		KindRenameTable:    false,
		KindAddColumn:      false,
		KindDropColumn:     true,
		KindRenameColumn:   false,
		KindAlterColumn:    true,
		KindAddIndex:       false,
		KindDropIndex:      false,
		KindAddForeignKey:  false,
		KindDropForeignKey: false,
		KindAddUnique:      false,
		KindDropUnique:     false,
		KindAddCheck:       false,
		KindDropCheck:      false,
		KindAddPrimaryKey:  false,
		KindDropPrimaryKey: false,
		KindCreateView:     false,
		KindDropView:       true,
		KindRaw:            true,
	}

	for _, k := range Kinds() {
		if _, ok := want[k]; !ok {
			t.Errorf("operation %q has no destructiveness verdict", k)
		}
	}

	for _, f := range fixtures(t) {
		if got := f.op.Destructive(); got != want[f.kind] {
			t.Errorf("%s.Destructive() = %v, want %v", f.kind, got, want[f.kind])
		}
	}
}

func TestAlterColumnIsDestructiveOnlyWhenRetyping(t *testing.T) {
	nullable := AlterColumn{Table: "users", Column: "email", Nullable: ptrBool(true)}
	if nullable.Destructive() {
		t.Error("relaxing nullability loses no data")
	}

	retyped := AlterColumn{Table: "users", Column: "email", Type: ptrType(t, "string(64)")}
	if !retyped.Destructive() {
		t.Error("a type change may narrow, and this op cannot tell; it must report destructive")
	}
}

func TestMarshalDoesNotEscapeHTML(t *testing.T) {
	op := AddCheck{Table: "orders", Name: "ck", Expression: `total >= 0 AND note <> '&'`}

	encoded, err := Marshal(op)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	for _, escaped := range []string{`\u003e`, `\u003c`, `\u0026`} {
		if strings.Contains(string(encoded), escaped) {
			t.Errorf("found the escape %s in %s", escaped, encoded)
			t.Log("Go escapes <, > and & by default; JavaScript's JSON.stringify does not.")
			t.Log("Escaping here would make the Go and TypeScript SDKs emit different bytes")
			t.Log("for the same migration, breaking ADR 0003 and changing the checksum.")
		}
	}

	if !strings.Contains(string(encoded), `total >= 0 AND note <> '&'`) {
		t.Errorf("the expression should survive verbatim, got %s", encoded)
	}
}

func TestMarshalEmitsTheTwoSeparatorsAsUTF8(t *testing.T) {
	query := "SELECT id, 98 AS rank" + string(lineSeparator) + "FROM users" +
		string(paragraphSeparator) + "WHERE active"

	encoded, err := Marshal(CreateView{View: "active_users", Query: query})
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	if bytes.Contains(encoded, separatorEscape) {
		t.Errorf("the separators were escaped: %s", encoded)
		t.Log("encoding/json escapes U+2028 and U+2029 whatever SetEscapeHTML is told, and")
		t.Log("JSON.stringify escapes neither. Escaping here makes the Go and TypeScript SDKs")
		t.Log("emit different bytes for one migration -- docs/ir.md §3.3, ADR 0047.")
	}

	if !bytes.Contains(encoded, []byte(query)) {
		t.Errorf("the query should survive verbatim, got %s", encoded)
		t.Log("The digits are load-bearing: a scanner that reads an escape by its last byte")
		t.Log("alone, rather than by the five before it, rewrites any string holding an 8 or")
		t.Log("a 9 six bytes in -- and a query with neither would never notice.")
	}
}

func TestMarshalLeavesAnAuthoredEscapeSequenceAlone(t *testing.T) {
	written := "SELECT id\\u2028FROM users"

	encoded, err := Marshal(CreateView{View: "active_users", Query: written})
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	decoded, err := UnmarshalOperation(encoded)
	if err != nil {
		t.Fatalf("decoding what Marshal emitted: %v", err)
	}

	if view, ok := decoded.(CreateView); !ok || view.Query != written {
		t.Errorf("a query holding the escape as text did not round-trip: %s", encoded)
		t.Log("Go writes an authored backslash doubled, so a ReplaceAll over the output would")
		t.Log("match the tail of that and leave a stray backslash before a raw separator --")
		t.Log("which is neither the document the author wrote nor valid JSON.")
	}
}

func TestAnOperationsArrayCarriesTheSeparatorsRawAsWell(t *testing.T) {
	op := CreateView{View: "active_users", Query: "SELECT id" + string(lineSeparator) + "FROM users"}

	alone, err := Marshal(op)
	if err != nil {
		t.Fatalf("marshaling the operation: %v", err)
	}

	array, err := Marshal(Operations{op})
	if err != nil {
		t.Fatalf("marshaling the array: %v", err)
	}

	if !bytes.Contains(array, alone) {
		t.Errorf("the array re-encoded the operation it was given\n got %s\nwant it to hold %s",
			array, alone)
		t.Log("An operation reaches the array as a json.RawMessage, which encoding/json compacts")
		t.Log("rather than re-encodes -- and appendCompact escapes the separators only when it")
		t.Log("is escaping HTML, which is what makes one rewrite at the leaf enough.")
	}
}

func TestSealedInterfaceCoversEveryKind(t *testing.T) {
	for _, f := range fixtures(t) {
		if f.op.Kind() != f.kind {
			t.Errorf("fixture %q holds an operation reporting %q", f.kind, f.op.Kind())
		}
	}

	if len(fixtures(t)) != len(Kinds()) {
		t.Errorf("fixtures cover %d kinds, the set has %d", len(fixtures(t)), len(Kinds()))
	}
}
