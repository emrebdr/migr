package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/schema"
)

// fixture is one operation paired with its canonical JSON document.
type fixture struct {
	kind Kind
	op   Operation
	json string
}

// fixtures returns one canonical example of every operation in the v1 set.
func fixtures(t *testing.T) []fixture {
	t.Helper()

	return []fixture{
		{
			kind: KindCreateTable,
			op: CreateTable{
				Table: "users",
				Columns: []Column{
					{Name: "id", Type: mustType(t, "uuid")},
					{Name: "email", Type: mustType(t, "string(255)")},
					{
						Name:     "created_at",
						Type:     mustType(t, "timestamptz"),
						Nullable: true,
						Default:  Call(FuncCurrentTimestamp),
					},
				},
				PrimaryKey: []string{"id"},
			},
			json: `{"op":"create_table","table":"users","columns":[` +
				`{"name":"id","type":"uuid","nullable":false},` +
				`{"name":"email","type":"string(255)","nullable":false},` +
				`{"name":"created_at","type":"timestamptz","nullable":true,` +
				`"default":{"function":"current_timestamp"}}],"primary_key":["id"]}`,
		},
		{
			kind: KindDropTable,
			op:   DropTable{Table: "users"},
			json: `{"op":"drop_table","table":"users"}`,
		},
		{
			kind: KindRenameTable,
			op:   RenameTable{From: "users", To: "accounts"},
			json: `{"op":"rename_table","from":"users","to":"accounts"}`,
		},
		{
			kind: KindAddColumn,
			op: AddColumn{
				Table: "users",
				Column: Column{
					Name:     "nickname",
					Type:     mustType(t, "string(64)"),
					Nullable: true,
					Default:  Literal("anonymous"),
				},
			},
			json: `{"op":"add_column","table":"users","column":{"name":"nickname",` +
				`"type":"string(64)","nullable":true,"default":{"literal":"anonymous"}}}`,
		},
		{
			kind: KindDropColumn,
			op:   DropColumn{Table: "users", Column: "nickname"},
			json: `{"op":"drop_column","table":"users","column":"nickname"}`,
		},
		{
			kind: KindRenameColumn,
			op:   RenameColumn{Table: "users", From: "email", To: "email_address"},
			json: `{"op":"rename_column","table":"users","from":"email","to":"email_address"}`,
		},
		{
			kind: KindAlterColumn,
			op: AlterColumn{
				Table:    "users",
				Column:   "email",
				Type:     ptrType(t, "string(512)"),
				Nullable: ptrBool(true),
			},
			json: `{"op":"alter_column","table":"users","column":"email",` +
				`"type":"string(512)","nullable":true}`,
		},
		{
			kind: KindAddIndex,
			op:   AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}},
			json: `{"op":"add_index","table":"users","name":"idx_users_email","columns":["email"]}`,
		},
		{
			kind: KindDropIndex,
			op:   DropIndex{Table: "users", Name: "idx_users_email"},
			json: `{"op":"drop_index","table":"users","name":"idx_users_email"}`,
		},
		{
			kind: KindAddForeignKey,
			op: AddForeignKey{
				Table:             "orders",
				Name:              "fk_orders_user",
				Columns:           []string{"user_id"},
				ReferencesTable:   "users",
				ReferencesColumns: []string{"id"},
				OnDelete:          ActionCascade,
				OnUpdate:          ActionRestrict,
			},
			json: `{"op":"add_foreign_key","table":"orders","name":"fk_orders_user",` +
				`"columns":["user_id"],"references_table":"users","references_columns":["id"],` +
				`"on_delete":"cascade","on_update":"restrict"}`,
		},
		{
			kind: KindDropForeignKey,
			op:   DropForeignKey{Table: "orders", Name: "fk_orders_user"},
			json: `{"op":"drop_foreign_key","table":"orders","name":"fk_orders_user"}`,
		},
		{
			kind: KindAddUnique,
			op:   AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}},
			json: `{"op":"add_unique","table":"users","name":"uq_users_email","columns":["email"]}`,
		},
		{
			kind: KindDropUnique,
			op:   DropUnique{Table: "users", Name: "uq_users_email"},
			json: `{"op":"drop_unique","table":"users","name":"uq_users_email"}`,
		},
		{
			kind: KindAddCheck,
			op:   AddCheck{Table: "orders", Name: "ck_orders_total", Expression: "total >= 0"},
			json: `{"op":"add_check","table":"orders","name":"ck_orders_total","expression":"total >= 0"}`,
		},
		{
			kind: KindDropCheck,
			op:   DropCheck{Table: "orders", Name: "ck_orders_total"},
			json: `{"op":"drop_check","table":"orders","name":"ck_orders_total"}`,
		},
		{
			kind: KindAddPrimaryKey,
			op:   AddPrimaryKey{Table: "legacy", Name: "pk_legacy", Columns: []string{"id"}},
			json: `{"op":"add_primary_key","table":"legacy","name":"pk_legacy","columns":["id"]}`,
		},
		{
			kind: KindDropPrimaryKey,
			op:   DropPrimaryKey{Table: "legacy", Name: "pk_legacy"},
			json: `{"op":"drop_primary_key","table":"legacy","name":"pk_legacy"}`,
		},
		{
			kind: KindCreateView,
			op: CreateView{
				View:      "active_users",
				Query:     "SELECT id FROM users WHERE active",
				OrReplace: true,
			},
			json: `{"op":"create_view","view":"active_users",` +
				`"query":"SELECT id FROM users WHERE active","or_replace":true}`,
		},
		{
			kind: KindDropView,
			op:   DropView{View: "active_users"},
			json: `{"op":"drop_view","view":"active_users"}`,
		},
		{
			kind: KindRaw,
			op:   Raw{Driver: "postgres", SQL: "CREATE EXTENSION IF NOT EXISTS pgcrypto"},
			json: `{"op":"raw","driver":"postgres","sql":"CREATE EXTENSION IF NOT EXISTS pgcrypto"}`,
		},
	}
}

func TestOperationsRoundTripThroughJSON(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(string(f.kind), func(t *testing.T) {
			encoded, err := Marshal(f.op)
			if err != nil {
				t.Fatalf("marshaling: %v", err)
			}

			if string(encoded) != f.json {
				t.Errorf("marshaled JSON differs from the canonical document:\n got %s\nwant %s",
					encoded, f.json)
			}

			decoded, err := UnmarshalOperation([]byte(f.json))
			if err != nil {
				t.Fatalf("unmarshaling the canonical document: %v", err)
			}

			if !reflect.DeepEqual(decoded, f.op) {
				t.Errorf("decoded value differs:\n got %#v\nwant %#v", decoded, f.op)
			}

			again, err := Marshal(decoded)
			if err != nil {
				t.Fatalf("re-marshaling: %v", err)
			}

			if string(again) != f.json {
				t.Errorf("bytes changed across a full round trip:\n got %s\nwant %s", again, f.json)
			}
		})
	}
}

func TestEveryKindHasAFixture(t *testing.T) {
	covered := make(map[Kind]bool, len(Kinds()))
	for _, f := range fixtures(t) {
		covered[f.kind] = true
	}

	const declared = 20

	if len(Kinds()) != declared {
		t.Fatalf("the v1 operation set has %d operations, Kinds() has %d", declared, len(Kinds()))
	}

	for _, k := range Kinds() {
		if !covered[k] {
			t.Errorf("operation %q has no fixture; M2-T4 walks this set for every driver", k)
		}
	}
}

func TestDiscriminatorMatchesTheGoType(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(string(f.kind), func(t *testing.T) {
			encoded, err := Marshal(f.op)
			if err != nil {
				t.Fatalf("marshaling: %v", err)
			}

			var d discriminator
			if err := json.Unmarshal(encoded, &d); err != nil {
				t.Fatalf("reading the discriminator: %v", err)
			}

			if d.Op != f.op.Kind() {
				t.Errorf(`marshaled "op" is %q but Kind() is %q`, d.Op, f.op.Kind())
			}

			if !strings.HasPrefix(string(encoded), `{"op":`) {
				t.Errorf("the discriminator should lead the document, got %s", encoded)
			}
		})
	}
}

func TestOperationListRoundTrips(t *testing.T) {
	all := make(Operations, 0, len(Kinds()))
	for _, f := range fixtures(t) {
		all = append(all, f.op)
	}

	encoded, err := Marshal(all)
	if err != nil {
		t.Fatalf("marshaling the list: %v", err)
	}

	var back Operations
	if err := Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshaling the list: %v", err)
	}

	if len(back) != len(all) {
		t.Fatalf("list length changed: %d -> %d", len(all), len(back))
	}

	for i := range all {
		if !reflect.DeepEqual(back[i], all[i]) {
			t.Errorf("operation %d (%s) changed across the round trip", i, all[i].Kind())
		}
	}

	again, err := Marshal(back)
	if err != nil {
		t.Fatalf("re-marshaling the list: %v", err)
	}

	if string(again) != string(encoded) {
		t.Error("the operation list is not byte-stable across a round trip")
	}
}

func TestMarshalingIsDeterministic(t *testing.T) {
	for _, f := range fixtures(t) {
		first, err := Marshal(f.op)
		if err != nil {
			t.Fatalf("%s: %v", f.kind, err)
		}

		for range 10 {
			next, err := Marshal(f.op)
			if err != nil {
				t.Fatalf("%s: %v", f.kind, err)
			}

			if string(next) != string(first) {
				t.Fatalf("%s marshals differently between runs", f.kind)
			}
		}
	}
}

func TestLogicalTypesReachTheWireAsStrings(t *testing.T) {
	encoded, err := Marshal(AddColumn{
		Table:  "users",
		Column: Column{Name: "price", Type: mustType(t, "decimal(10,2)")},
	})
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}

	if !strings.Contains(string(encoded), `"type":"decimal(10,2)"`) {
		t.Errorf("ADR 0005 requires the canonical string form, got %s", encoded)
	}
}

func TestNoOperationFieldReachesTheWireAsAFloat(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(string(f.kind), func(t *testing.T) {
			if path := findFloat(reflect.TypeOf(f.op), string(f.kind), 0); path != "" {
				t.Errorf("%s is a floating-point field", path)
				t.Log("Go renders floats with the shortest representation that round-trips and")
				t.Log("switches to exponent form at its own thresholds; JavaScript's Number")
				t.Log("formatting agrees on many values and not on all of them. One float on the")
				t.Log("wire would break byte-identical IR across SDKs and move the checksum with")
				t.Log("it. Every v1 field is a string, a bool, or a list of them, and a logical")
				t.Log("type marshals as text -- no JSON number reaches the hashed bytes at all.")
			}
		})
	}
}

// findFloat returns the path to the first floating-point field in a type, or empty.
func findFloat(typ reflect.Type, path string, depth int) string {
	const maxDepth = 8
	if depth > maxDepth {
		return ""
	}

	switch typ.Kind() {
	case reflect.Float32, reflect.Float64:
		return path
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return findFloat(typ.Elem(), path+"[]", depth+1)
	case reflect.Struct:
		return findFloatInStruct(typ, path, depth)
	default:
		return ""
	}
}

// findFloatInStruct walks a struct's fields, returning the first floating-point path.
func findFloatInStruct(typ reflect.Type, path string, depth int) string {
	for i := range typ.NumField() {
		field := typ.Field(i)
		if found := findFloat(field.Type, path+"."+field.Name, depth+1); found != "" {
			return found
		}
	}

	return ""
}

// mustType parses a canonical type string that the test author knows is valid.
func mustType(t *testing.T, s string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(s)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", s, err)
	}

	return parsed
}

// ptrType parses a canonical type string and returns a pointer to it.
func ptrType(t *testing.T, s string) *schema.Type {
	t.Helper()

	parsed := mustType(t, s)

	return &parsed
}

// ptrBool returns a pointer to the given bool, for the tri-state alter_column fields.
func ptrBool(v bool) *bool {
	return &v
}
