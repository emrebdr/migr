package sqlite

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/schema"
)

// The reads one snapshot is built from: one catalog table and four pragmas, plus what they omit.
//
// Everything SQLite names for itself is excluded -- sqlite_sequence, sqlite_stat1 -- and so is the
// table migr records its own runs in, which a differ would otherwise propose dropping. tbl_name is
// what excludes a table and everything attached to it in one predicate.
const (
	objectsQuery = `SELECT type, name, coalesce(sql, '') FROM sqlite_schema ` +
		`WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' AND tbl_name <> ? ` +
		`ORDER BY name`

	tableColumnsQuery = `SELECT name, type, "notnull", coalesce(dflt_value, ''), pk, hidden ` +
		`FROM pragma_table_xinfo(?)`

	indexListQuery = `SELECT name, "unique", origin, partial FROM pragma_index_list(?)`

	indexColumnsQuery = `SELECT coalesce(name, ''), cid, "desc" FROM pragma_index_xinfo(?) ` +
		`WHERE key = 1 ORDER BY seqno`

	foreignKeysQuery = `SELECT id, "table", "from", coalesce("to", ''), on_update, on_delete ` +
		`FROM pragma_foreign_key_list(?) ORDER BY id, seq`
)

// The pragma_table_xinfo hidden values, which are what a generated column is visible through at all.
const (
	hiddenNone    = 0
	hiddenVirtual = 1
	hiddenGenVirt = 2
	hiddenGenStor = 3
)

// originPrimaryKey is the pragma_index_list origin of an index SQLite made for a primary key.
const originPrimaryKey = "pk"

// expressionColumn is the pragma_index_xinfo cid of a key entry that is not a column at all.
const expressionColumn = -2

// Introspect reads this database's one namespace back, less the table migr records its own runs in.
func (c *sqliteConn) Introspect(ctx context.Context) (*schema.Snapshot, error) {
	objects, err := c.readObjects(ctx)
	if err != nil {
		return nil, err
	}

	snapshot := &schema.Snapshot{}

	for _, found := range objects {
		if err := c.addObject(ctx, snapshot, found); err != nil {
			return nil, err
		}
	}

	if err := snapshot.Validate(); err != nil {
		return nil, fmt.Errorf("%w: this driver read a snapshot it cannot stand behind: %w",
			ErrIntrospect, err)
	}

	return snapshot, nil
}

// object is one thing the catalog holds, which is a table or a view and nothing else here.
type object struct {
	// kind is what SQLite calls it: table or view.
	kind string
	// name is its own name, which is unique across both kinds in one database.
	name string
	// recorded is the CREATE statement SQLite kept, which is the only record of several things.
	recorded string
}

// readObjects lists the tables and views this database holds, in the order names sort.
func (c *sqliteConn) readObjects(ctx context.Context) ([]object, error) {
	rows, err := c.read(ctx, objectsQuery, c.table)
	if err != nil {
		return nil, err
	}

	found := make([]object, 0, len(rows.Values))
	for _, row := range rows.Values {
		found = append(found, object{
			kind: valueOf(row[0]), name: valueOf(row[1]), recorded: valueOf(row[2]),
		})
	}

	return found, nil
}

// addObject reads one object back into the snapshot being built.
func (c *sqliteConn) addObject(ctx context.Context, into *schema.Snapshot, found object) error {
	if found.kind == "view" {
		into.Views = append(into.Views, viewOf(found))

		return nil
	}

	table, err := c.readTable(ctx, found)
	if err != nil {
		return err
	}

	into.Tables = append(into.Tables, table)

	return nil
}

// viewOf reads a view back, carrying the body alone, since the statement around it is not portable.
func viewOf(found object) schema.View {
	body, extracted := viewBody(found.recorded)
	if !extracted {
		return schema.View{Name: found.name, Query: found.recorded, Beyond: &schema.Unrepresented{
			Detail: "SQLite records its definition as " + strconv.Quote(found.recorded) +
				", which this driver cannot find the query inside"}}
	}

	return schema.View{Name: found.name, Query: body}
}

// viewBody returns the query a recorded CREATE VIEW defines, which is what every engine reports.
func viewBody(recorded string) (string, bool) {
	body, found := afterKeyword(recorded, "AS")
	if !found || strings.TrimSpace(body) == "" {
		return "", false
	}

	return strings.TrimSpace(body), true
}

// readTable reads one table back from its pragmas and the definition SQLite recorded for it.
func (c *sqliteConn) readTable(ctx context.Context, found object) (schema.Table, error) {
	declared, unreadable := declaredItems(found)

	table := schema.Table{Name: found.name}
	beyond := beyondOf(found.recorded, unreadable)

	columns, key, err := c.readColumns(ctx, found.name, declared)
	if err != nil {
		return schema.Table{}, err
	}

	table.Columns, table.PrimaryKey = columns, namedKey(key, declared)
	table.Checks = checksOf(declared, columns)

	indexes, uniques, unrepresentable, err := c.readIndexes(ctx, found.name)
	if err != nil {
		return schema.Table{}, err
	}

	table.Indexes, table.Uniques = indexes, uniques
	beyond = append(beyond, unrepresentable...)

	keys, unpaired, err := c.readForeignKeys(ctx, found.name, declared)
	if err != nil {
		return schema.Table{}, err
	}

	table.ForeignKeys = keys
	table.Beyond = marked(append(beyond, unpaired...))

	return table, nil
}

// declaredItems takes a recorded definition apart, reporting one this driver cannot read.
func declaredItems(found object) ([]item, string) {
	definition, err := splitTable(found.name, found.recorded)
	if err != nil {
		return nil, fmt.Sprintf("SQLite records its definition as %s, which this driver cannot "+
			"take apart, so its checks and its constraint names are not in this snapshot",
			strconv.Quote(found.recorded))
	}

	return classifyItems(definition.items), ""
}

// beyondOf starts a table's findings with what its recorded definition says around the column list.
func beyondOf(recorded, unreadable string) []string {
	var found []string

	if unreadable != "" {
		found = append(found, unreadable)
	}

	closing := closingOf(recorded)

	for _, keyword := range [][]string{{"WITHOUT", "ROWID"}, {"STRICT"}} {
		if declaresWords(closing, keyword...) {
			found = append(found, "its definition declares "+strings.Join(keyword, " ")+
				", which no operation can carry")
		}
	}

	return found
}

// closingOf returns what a recorded definition says after its column list, which is where it says it.
func closingOf(recorded string) string {
	open := indexAtDepth(recorded, openParen)
	if open < 0 {
		return ""
	}

	shut := matchingParen(recorded, open)
	if shut < 0 {
		return ""
	}

	return recorded[shut+1:]
}

// marked returns the one marker a set of findings makes, and nothing at all where there are none.
func marked(found []string) *schema.Unrepresented {
	if len(found) == 0 {
		return nil
	}

	return &schema.Unrepresented{Detail: strings.Join(found, "; ")}
}

// keyColumn is one column a table's primary key covers, and where in the key it sits.
type keyColumn struct {
	// name is the column's own name.
	name string
	// at is the pragma's own position for it, counting from one.
	at int
}

// readColumns reads a table's columns and the key they declare, which one pragma answers both of.
func (c *sqliteConn) readColumns(ctx context.Context, table string,
	declared []item,
) ([]schema.Column, []keyColumn, error) {
	rows, err := c.read(ctx, tableColumnsQuery, table)
	if err != nil {
		return nil, nil, err
	}

	var (
		columns []schema.Column
		key     []keyColumn
	)

	for _, row := range rows.Values {
		column, position, err := columnOf(row, declared)
		if err != nil {
			return nil, nil, fmt.Errorf("%w of table %q: %w", ErrIntrospect, table, err)
		}

		columns = append(columns, column)

		if position > 0 {
			key = append(key, keyColumn{name: column.Name, at: position})
		}
	}

	return columns, key, nil
}

// columnOf reads one pragma row back as a column, and reports where in the primary key it sits.
func columnOf(row []*string, declared []item) (schema.Column, int, error) {
	name := valueOf(row[0])

	notNull, err := pragmaNumber(row[2], "notnull")
	if err != nil {
		return schema.Column{}, 0, err
	}

	position, err := pragmaNumber(row[4], "pk")
	if err != nil {
		return schema.Column{}, 0, err
	}

	hidden, err := pragmaNumber(row[5], "hidden")
	if err != nil {
		return schema.Column{}, 0, err
	}

	own, _ := columnItem(declared, name)

	built, err := buildColumn(name, valueOf(row[1]), valueOf(row[3]), notNull == 0, hidden, own)
	if err != nil {
		return schema.Column{}, 0, err
	}

	return built, position, nil
}

// buildColumn assembles one column from what the pragma answered and what its own definition says.
func buildColumn(name, declared, recorded string, nullable bool, hidden int,
	own item,
) (schema.Column, error) {
	typed, err := columnType(declared, absorbed(name, declared, own))
	if err != nil {
		return schema.Column{}, fmt.Errorf("column %q: %w", name, err)
	}

	return schema.Column{
		Name:     name,
		Type:     typed,
		Nullable: nullable,
		Default:  literalDefault(recorded),
		Beyond:   marked(columnFindings(declared, hidden, own)),
	}, nil
}

// columnFindings lists what this column carries that no operation can express.
func columnFindings(declared string, hidden int, own item) []string {
	var found []string

	switch hidden {
	case hiddenGenVirt:
		found = append(found, "SQLite records it as a virtual generated column, which no operation "+
			"can create -- pragma_table_info omits it entirely")
	case hiddenGenStor:
		found = append(found, "SQLite records it as a stored generated column, which no operation "+
			"can create -- pragma_table_info omits it entirely")
	case hiddenVirtual:
		found = append(found, "SQLite records it as a hidden column of a virtual table")
	case hiddenNone:
	}

	if strings.TrimSpace(declared) == "" {
		found = append(found, "it is declared with no type at all, so SQLite gives it "+
			noDeclaredType+" affinity and no logical type names it")
	}

	return append(found, declaredFindings(own)...)
}

// declaredFindings lists what a column's own recorded definition declares that nothing can carry.
func declaredFindings(own item) []string {
	var found []string

	for _, keyword := range []string{"COLLATE", "AUTOINCREMENT"} {
		if declaresWords(own.text, keyword) {
			found = append(found, "its definition declares "+keyword+
				", which no operation can carry")
		}
	}

	return found
}

// namedKey returns the primary key a table's columns declare, under the name its definition gives it.
func namedKey(key []keyColumn, declared []item) *schema.PrimaryKey {
	if len(key) == 0 {
		return nil
	}

	slices.SortStableFunc(key, func(a, b keyColumn) int { return a.at - b.at })

	columns := make([]string, 0, len(key))
	for _, covered := range key {
		columns = append(columns, covered.name)
	}

	return &schema.PrimaryKey{Name: constraintName(declared, keywordPrimary), Columns: columns}
}

// read runs one of this file's probes on the pinned connection, inside a transaction or outside one.
func (c *sqliteConn) read(ctx context.Context, probe string, args ...any) (driver.Rows, error) {
	rows, err := c.runner().QueryContext(ctx, probe, args...)
	if err != nil {
		return driver.Rows{}, fmt.Errorf("%w of %q: %w", ErrIntrospect, c.describePath(), err)
	}

	defer func() { _ = rows.Close() }()

	collected, err := collect(rows)
	if err != nil {
		return driver.Rows{}, fmt.Errorf("%w of %q: %w", ErrIntrospect, c.describePath(), err)
	}

	return collected, nil
}

// describePath names the database a failure happened against, an in-memory one having no path.
func (c *sqliteConn) describePath() string {
	if c.path == "" {
		return "an in-memory database"
	}

	return c.path
}

// pragmaNumber reads a pragma's integer column, which is answered as text like every other value here.
func pragmaNumber(value *string, column string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(valueOf(value)))
	if err != nil {
		return 0, fmt.Errorf("the catalog answered %q for %s, which is not a number: %w",
			valueOf(value), column, err)
	}

	return parsed, nil
}
