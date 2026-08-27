package runtime

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// tree is the fixture set itself, embedded so a caller runs the suite from any directory.
//
//go:embed testdata
var tree embed.FS

// fixtureDir is where the neutral definitions live, relative to this package.
const fixtureDir = "testdata/fixtures"

// sourceDir is where each language's expression of those definitions lives.
const sourceDir = "testdata/sources"

// NoDownAuthored is the refusal a fixture that authors no undo of its own must produce.
const NoDownAuthored = "no_down_authored"

// DeclaredIrreversible is the refusal a fixture whose author stated it is one-way must produce.
const DeclaredIrreversible = "declared_irreversible"

// Read is one host.db.query a fixture makes, and what every SDK is told it returned.
type Read struct {
	// SQL is the statement the migration wrote itself, which every language must send verbatim.
	SQL string `json:"sql"`
	// Args are its bound parameters, positional and textual.
	Args []string `json:"args,omitempty"`
	// Columns names what comes back, in order.
	Columns []string `json:"columns"`
	// Rows are the values, as the server's own text, with null written as JSON null.
	Rows [][]*string `json:"rows"`
}

// Write is one host.db.exec a fixture makes, and what an apply reports it changed.
type Write struct {
	// SQL is the statement the migration wrote itself, which every language must send verbatim.
	SQL string `json:"sql"`
	// Args are its bound parameters, positional and textual.
	Args []string `json:"args,omitempty"`
	// RowsAffected is what the server reports, as text, since a count may exceed 2^53.
	RowsAffected string `json:"rowsAffected"`
}

// Refusal is §7.4's answer for a migration that has no undo, and the reason when its author gave one.
type Refusal struct {
	// Code is NoDownAuthored or DeclaredIrreversible, and nothing else.
	Code string `json:"code"`
	// Reason is the author's own, which only a declaration carries.
	Reason string `json:"reason,omitempty"`
}

// Fixture is one canonical migration: what it is called, and what every SDK must emit for it.
type Fixture struct {
	// Version is the UTC timestamp its filename carries, which every language shares.
	Version string
	// Name is the snake_case name, which is the rest of that filename.
	Name string
	// Up is what the migration performs, decoded so the suite can say which kinds it covers.
	Up ir.Operations
	// Down is what undoes it, empty when the fixture states a refusal instead.
	Down ir.Operations
	// Refusal is §7.4's answer this fixture must produce instead of a down, or nil.
	Refusal *Refusal
	// Reads are the queries it makes, in order, with what the host answers each one.
	Reads []Read
	// Writes are the statements it sends itself, in order, with what an apply reports.
	Writes []Write

	// up is the manifest's own bytes for Up, compacted, which is what an SDK is compared against.
	up []byte
	// down is the same for Down, and empty for a fixture that states a refusal.
	down []byte
}

// Stem renders the filename every language stores this fixture under, bar its own extension.
func (f Fixture) Stem() string {
	return f.Version + "_" + f.Name
}

// Expected returns the bytes an SDK must emit for one direction of this fixture.
func (f Fixture) Expected(dir ir.Direction) []byte {
	if dir == ir.Down {
		return f.down
	}

	return f.up
}

// Operations returns the operations one direction of this fixture is defined to emit.
func (f Fixture) Operations(dir ir.Direction) ir.Operations {
	if dir == ir.Down {
		return f.Down
	}

	return f.Up
}

// manifest is the JSON shape one fixture file carries.
type manifest struct {
	Up          json.RawMessage `json:"up"`
	Down        json.RawMessage `json:"down,omitempty"`
	DownRefusal *Refusal        `json:"downRefusal,omitempty"`
	Reads       []Read          `json:"reads,omitempty"`
	Writes      []Write         `json:"writes,omitempty"`
}

// ErrNoFixtures marks a fixture directory with nothing in it, which every SDK would pass.
var ErrNoFixtures = errors.New("the fixture set is empty, so passing it proves nothing")

// ErrNoSources marks a language this fixture set carries no expression of.
var ErrNoSources = errors.New("this fixture set carries no sources for that language")

// Fixtures returns every canonical migration, in version order, or why one could not be read.
func Fixtures() ([]Fixture, error) {
	entries, err := fs.ReadDir(tree, fixtureDir)
	if err != nil {
		return nil, fmt.Errorf("reading the fixture set: %w", err)
	}

	loaded := make([]Fixture, 0, len(entries))

	for _, entry := range entries {
		f, err := load(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}

		loaded = append(loaded, f)
	}

	return order(loaded)
}

// order puts a fixture set in version order, refusing one that would hold a language to nothing.
func order(loaded []Fixture) ([]Fixture, error) {
	if len(loaded) == 0 {
		return nil, ErrNoFixtures
	}

	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Version < loaded[j].Version })

	return loaded, nil
}

// Sources returns one language's expression of every fixture, for a caller to write into a project.
func Sources(language string) (fs.FS, error) {
	dir := path.Join(sourceDir, language)

	if _, err := fs.Stat(tree, dir); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSources, language)
	}

	sub, err := fs.Sub(tree, dir)
	if err != nil {
		return nil, fmt.Errorf("opening the %s sources: %w", language, err)
	}

	return sub, nil
}

// load reads one fixture file, refusing a definition no SDK could be held to.
func load(filename string) (Fixture, error) {
	named, err := migration.Parse(filename)
	if err != nil {
		return Fixture{}, err
	}

	raw, err := tree.ReadFile(path.Join(fixtureDir, filename))
	if err != nil {
		return Fixture{}, err
	}

	var m manifest
	if err := strictUnmarshal(raw, &m); err != nil {
		return Fixture{}, err
	}

	return build(Fixture{Version: named.Version, Name: named.Name}, m)
}

// build decodes both directions and holds the manifest itself to being canonical IR.
func build(f Fixture, m manifest) (Fixture, error) {
	var err error

	f.Refusal, f.Reads, f.Writes = m.DownRefusal, m.Reads, m.Writes

	if f.Up, f.up, err = canonical("up", m.Up); err != nil {
		return Fixture{}, err
	}

	if err := checkDown(m); err != nil {
		return Fixture{}, err
	}

	if err := checkRefusal(m.DownRefusal); err != nil {
		return Fixture{}, err
	}

	if len(m.Down) > 0 {
		if f.Down, f.down, err = canonical("down", m.Down); err != nil {
			return Fixture{}, err
		}
	}

	return f, checkStatements(f)
}

// checkDown reports a fixture that states both an undo and a refusal, or neither.
func checkDown(m manifest) error {
	if len(m.Down) > 0 && m.DownRefusal != nil {
		return errors.New("down and downRefusal cannot both be set: a migration authors an undo " +
			"or reports it has none")
	}

	if len(m.Down) == 0 && m.DownRefusal == nil {
		return errors.New("neither down nor downRefusal is set, so the fixture says nothing " +
			"about what undoes it")
	}

	return nil
}

// checkRefusal reports a refusal naming no answer §7.4 defines, or a declaration with no reason.
func checkRefusal(r *Refusal) error {
	if r == nil {
		return nil
	}

	switch r.Code {
	case NoDownAuthored:
		if r.Reason != "" {
			return fmt.Errorf("%s carries a reason, and only %s does", NoDownAuthored,
				DeclaredIrreversible)
		}
	case DeclaredIrreversible:
		if strings.TrimSpace(r.Reason) == "" {
			return fmt.Errorf("%s gives the reason a rollback halts with", DeclaredIrreversible)
		}
	default:
		return fmt.Errorf("downRefusal %q names neither %s nor %s", r.Code, NoDownAuthored,
			DeclaredIrreversible)
	}

	return nil
}

// checkStatements reports a read or a write carrying no SQL for an SDK to send.
func checkStatements(f Fixture) error {
	for i, read := range f.Reads {
		if strings.TrimSpace(read.SQL) == "" {
			return fmt.Errorf("reads[%d] carries no SQL", i)
		}
	}

	for i, write := range f.Writes {
		if strings.TrimSpace(write.SQL) == "" {
			return fmt.Errorf("writes[%d] carries no SQL", i)
		}
	}

	return nil
}

// canonical decodes one direction and reports the bytes an SDK is compared against.
func canonical(field string, raw json.RawMessage) (ir.Operations, []byte, error) {
	var ops ir.Operations
	if err := json.Unmarshal(raw, &ops); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", field, err)
	}

	if len(ops) == 0 {
		return nil, nil, fmt.Errorf("%s emits nothing, and a migration that changes nothing is "+
			"not a fixture", field)
	}

	var compacted bytes.Buffer
	if err := json.Compact(&compacted, raw); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", field, err)
	}

	emitted, err := ir.Marshal(ops)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", field, err)
	}

	if !bytes.Equal(compacted.Bytes(), emitted) {
		return nil, nil, describeUncanonical(field, compacted.Bytes(), emitted)
	}

	return ops, compacted.Bytes(), nil
}

// describeUncanonical reports a fixture whose own bytes are not the bytes it demands.
func describeUncanonical(field string, written, emitted []byte) error {
	return fmt.Errorf("%s is not written the way the IR is emitted, so no SDK could match it "+
		"byte for byte\n written %s\nemitted %s", field, written, emitted)
}

// strictUnmarshal decodes a manifest, refusing a member the fixture format does not define.
func strictUnmarshal(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("reading the fixture: %w", err)
	}

	return nil
}
