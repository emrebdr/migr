package checksum

import (
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

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

// asciiSource is shared by the baseline vector and its CRLF twin.
const asciiSource = "export function up(s: Schema) {\n  s.dropTable(\"users\");\n}\n"

// The two separators docs/ir.md §3.3 emits as UTF-8, by code point so this source stays readable.
const (
	lineSeparator      = string(rune(0x2028))
	paragraphSeparator = string(rune(0x2029))
)

// vector is one committed known-answer case: two inputs and every byte they produce.
type vector struct {
	name       string
	source     string
	ops        ir.Operations
	normalized string
	encoded    string
	checksum   string
}

// vectors returns the committed checksum contract, derived from the spec, not from this code.
func vectors(t *testing.T) []vector {
	t.Helper()

	return []vector{
		{
			name:       "ascii",
			source:     asciiSource,
			ops:        ir.Operations{ir.DropTable{Table: "users"}},
			normalized: asciiSource,
			encoded:    `[{"op":"drop_table","table":"users"}]`,
			checksum: "migr1:26722f80c5e09f62322c2209fe39d237a71042ed918c3706b7e6fd5d77a5a801" +
				":dd7a56777f2b0905b433555bebccc54265419321280f205980138fde99ce7e24",
		},
		{
			name:   "htmlChars",
			source: "CHECK (total >= 0 AND name <> '' AND flags & 1)\n",
			ops: ir.Operations{ir.AddCheck{
				Table:      "orders",
				Name:       "ck_orders_total",
				Expression: "total >= 0 AND name <> '' AND flags & 1",
			}},
			normalized: "CHECK (total >= 0 AND name <> '' AND flags & 1)\n",
			encoded: `[{"op":"add_check","table":"orders","name":"ck_orders_total",` +
				`"expression":"total >= 0 AND name <> '' AND flags & 1"}]`,
			checksum: "migr1:b1bad082faed98f8229cece91214625d2072fea4d5e2d0fb73937b3a6c6e2574" +
				":f14f0cab508c8b6e897d1afc8a1e4d081c2bdba5aa7e0083a1da8bc67cb7eaa0",
		},
		{
			name:   "lineSeparators",
			source: "// a view query carrying U+2028 and U+2029\n",
			ops: ir.Operations{ir.CreateView{
				View: "active_users",
				Query: "SELECT id" + lineSeparator + "FROM users" +
					paragraphSeparator + "WHERE active",
			}},
			normalized: "// a view query carrying U+2028 and U+2029\n",
			encoded: `[{"op":"create_view","view":"active_users",` +
				`"query":"SELECT id` + lineSeparator + `FROM users` + paragraphSeparator +
				`WHERE active"}]`,
			checksum: "migr1:ead95624e0fd5a5230c23451754ec41fbf20bb629b761db6bd924163e4fd5b4c" +
				":6182bc8567542e93bcbcbcc38472bbd024f28b5323931ce3e1faa504ad0a1a9f",
		},
		{
			name:   "nonASCII",
			source: "// tablo: müşteriler\n",
			ops: ir.Operations{ir.AddCheck{
				Table:      "müşteriler",
				Name:       "ck_bakiye",
				Expression: "bakiye >= 0 -- 🎉",
			}},
			normalized: "// tablo: müşteriler\n",
			encoded: `[{"op":"add_check","table":"müşteriler","name":"ck_bakiye",` +
				`"expression":"bakiye >= 0 -- 🎉"}]`,
			checksum: "migr1:16cbbfaa26eadd74bdf07bbd11431530710aca3bd699cc0b3f12299e341fc540" +
				":04de986c6c5b42344933b50b2942b3ae2e2d79c02067bd343aba24666536eb78",
		},
		{
			name:       "crlfSource",
			source:     "export function up(s: Schema) {\r\n  s.dropTable(\"users\");\r\n}\r\n",
			ops:        ir.Operations{ir.DropTable{Table: "users"}},
			normalized: asciiSource,
			encoded:    `[{"op":"drop_table","table":"users"}]`,
			checksum: "migr1:26722f80c5e09f62322c2209fe39d237a71042ed918c3706b7e6fd5d77a5a801" +
				":dd7a56777f2b0905b433555bebccc54265419321280f205980138fde99ce7e24",
		},
		{
			name:       "emptyOperations",
			source:     "export function down(s: Schema) {}\n",
			ops:        ir.Operations{},
			normalized: "export function down(s: Schema) {}\n",
			encoded:    `[]`,
			checksum: "migr1:748e4382cf90a713ab13e9d8e022d025da8d944feed91bb7b2538df9a511aa76" +
				":4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945",
		},
		{
			name:       "everyOperation",
			source:     "// one instance of every operation in the v1 set\n",
			ops:        everyOperation(t),
			normalized: "// one instance of every operation in the v1 set\n",
			encoded:    everyOperationJSON,
			checksum: "migr1:3dfe478d660be923be277be8362ba8b0ee394b49e564274963542449de6ab042" +
				":72fffa276e12aab0165821a8d0e4f813efbfbc125fcc8611217b8e47ce9d1177",
		},
	}
}

// everyOperation returns one instance of each operation in the v1 set, in a fixed order.
func everyOperation(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{
			Table: "users",
			Columns: []ir.Column{
				{Name: "id", Type: mustType(t, "uuid")},
				{Name: "email", Type: mustType(t, "string(255)")},
				{
					Name:     "created_at",
					Type:     mustType(t, "timestamptz"),
					Nullable: true,
					Default:  ir.Call(ir.FuncCurrentTimestamp),
				},
			},
			PrimaryKey: []string{"id"},
		},
		ir.DropTable{Table: "users"},
		ir.RenameTable{From: "users", To: "accounts"},
		ir.AddColumn{Table: "users", Column: ir.Column{
			Name:     "nickname",
			Type:     mustType(t, "string(64)"),
			Nullable: true,
			Default:  ir.Literal("anonymous"),
		}},
		ir.DropColumn{Table: "users", Column: "nickname"},
		ir.RenameColumn{Table: "users", From: "email", To: "email_address"},
		ir.AlterColumn{
			Table:    "users",
			Column:   "email",
			Type:     ptrType(t, "string(512)"),
			Nullable: ptrBool(true),
		},
		ir.AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}},
		ir.DropIndex{Table: "users", Name: "idx_users_email"},
		ir.AddForeignKey{
			Table:             "orders",
			Name:              "fk_orders_user",
			Columns:           []string{"user_id"},
			ReferencesTable:   "users",
			ReferencesColumns: []string{"id"},
			OnDelete:          ir.ActionCascade,
			OnUpdate:          ir.ActionRestrict,
		},
		ir.DropForeignKey{Table: "orders", Name: "fk_orders_user"},
		ir.AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}},
		ir.DropUnique{Table: "users", Name: "uq_users_email"},
		ir.AddCheck{Table: "orders", Name: "ck_orders_total", Expression: "total >= 0"},
		ir.DropCheck{Table: "orders", Name: "ck_orders_total"},
		ir.AddPrimaryKey{Table: "legacy", Name: "pk_legacy", Columns: []string{"id"}},
		ir.DropPrimaryKey{Table: "legacy", Name: "pk_legacy"},
		ir.CreateView{
			View:      "active_users",
			Query:     "SELECT id FROM users WHERE active",
			OrReplace: true,
		},
		ir.DropView{View: "active_users"},
		ir.Raw{Driver: "postgres", SQL: "CREATE EXTENSION IF NOT EXISTS pgcrypto"},
	}
}

// everyOperationJSON is the committed encoding of the full v1 set, one document per operation.
const everyOperationJSON = `[` +
	`{"op":"create_table","table":"users","columns":[` +
	`{"name":"id","type":"uuid","nullable":false},` +
	`{"name":"email","type":"string(255)","nullable":false},` +
	`{"name":"created_at","type":"timestamptz","nullable":true,` +
	`"default":{"function":"current_timestamp"}}],"primary_key":["id"]},` +
	`{"op":"drop_table","table":"users"},` +
	`{"op":"rename_table","from":"users","to":"accounts"},` +
	`{"op":"add_column","table":"users","column":{"name":"nickname",` +
	`"type":"string(64)","nullable":true,"default":{"literal":"anonymous"}}},` +
	`{"op":"drop_column","table":"users","column":"nickname"},` +
	`{"op":"rename_column","table":"users","from":"email","to":"email_address"},` +
	`{"op":"alter_column","table":"users","column":"email",` +
	`"type":"string(512)","nullable":true},` +
	`{"op":"add_index","table":"users","name":"idx_users_email","columns":["email"]},` +
	`{"op":"drop_index","table":"users","name":"idx_users_email"},` +
	`{"op":"add_foreign_key","table":"orders","name":"fk_orders_user",` +
	`"columns":["user_id"],"references_table":"users","references_columns":["id"],` +
	`"on_delete":"cascade","on_update":"restrict"},` +
	`{"op":"drop_foreign_key","table":"orders","name":"fk_orders_user"},` +
	`{"op":"add_unique","table":"users","name":"uq_users_email","columns":["email"]},` +
	`{"op":"drop_unique","table":"users","name":"uq_users_email"},` +
	`{"op":"add_check","table":"orders","name":"ck_orders_total","expression":"total >= 0"},` +
	`{"op":"drop_check","table":"orders","name":"ck_orders_total"},` +
	`{"op":"add_primary_key","table":"legacy","name":"pk_legacy","columns":["id"]},` +
	`{"op":"drop_primary_key","table":"legacy","name":"pk_legacy"},` +
	`{"op":"create_view","view":"active_users",` +
	`"query":"SELECT id FROM users WHERE active","or_replace":true},` +
	`{"op":"drop_view","view":"active_users"},` +
	`{"op":"raw","driver":"postgres","sql":"CREATE EXTENSION IF NOT EXISTS pgcrypto"}]`

func TestTheCommittedVectorsStillHold(t *testing.T) {
	for _, v := range vectors(t) {
		t.Run(v.name, func(t *testing.T) {
			if got := string(normalize([]byte(v.source))); got != v.normalized {
				t.Errorf("normalized source =\n %q\nwant\n %q", got, v.normalized)
			}

			encoded, err := ir.Marshal(v.ops)
			if err != nil {
				t.Fatalf("ir.Marshal: %v", err)
			}

			if string(encoded) != v.encoded {
				t.Errorf("emitted IR =\n %s\nwant\n %s", encoded, v.encoded)
			}

			if got := mustOf(t, []byte(v.source), v.ops).String(); got != v.checksum {
				t.Errorf("checksum =\n %s\nwant\n %s", got, v.checksum)
				explainVector(t)
			}
		})
	}
}

// explainVector states what a moved vector means, so nobody regenerates the constant.
func explainVector(t *testing.T) {
	t.Helper()

	t.Log("These values are a committed contract, derived from the migr1 spec by a second")
	t.Log("implementation rather than from this package. If one changed, every applied")
	t.Log("migration in every installation now reads as drifted -- exit code 4, and")
	t.Log("invariant #6 offers no flag that repairs it.")
	t.Log("Do not regenerate this constant. Find out what moved: the normalized-source and")
	t.Log("emitted-IR assertions above say which of the two inputs it was.")
	t.Log("One vector has moved, once: lineSeparators, in M7-T9, when ir.Marshal stopped")
	t.Log("escaping U+2028 and U+2029 (ADR 0047). Its new value was derived by hashing the")
	t.Log("bytes by hand rather than by running this package. Nothing else has moved, and a")
	t.Log("decision recorded in an ADR is the only thing that may move one again.")
}

func TestTheCRLFVectorAgreesWithItsLineFeedTwin(t *testing.T) {
	var ascii, crlf vector

	for _, v := range vectors(t) {
		switch v.name {
		case "ascii":
			ascii = v
		case "crlfSource":
			crlf = v
		}
	}

	if ascii.source == crlf.source {
		t.Fatal("the CRLF vector must differ from its twin in raw bytes")
	}

	if ascii.checksum != crlf.checksum {
		t.Error("the same file checked out on Windows and on Linux produced two checksums")
		t.Log("Both committed values are written out in full above rather than computed,")
		t.Log("so this compares two constants, not an implementation against itself.")
	}
}
