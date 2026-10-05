package driver

import (
	"context"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/schema"
)

// answering is a session that runs every statement and answers with the snapshot a test staged.
type answering struct {
	*fakeConn

	// live is what Introspect answers, which stands in for a catalog this suite never reaches.
	live *schema.Snapshot
	// ran counts the statements this session was given, so a test can see the fixture was applied.
	ran int
}

// Exec accepts every statement, since what this suite is about is what comes back afterwards.
func (a *answering) Exec(context.Context, driver.Statement) error {
	a.ran++

	return nil
}

// Introspect answers with the snapshot this test staged.
func (a *answering) Introspect(context.Context) (*schema.Snapshot, error) {
	if a.live == nil {
		return &schema.Snapshot{}, nil
	}

	return a.live, nil
}

// snapshotSuite pairs a fake driver with a session answering one staged snapshot.
func snapshotSuite(t *testing.T, live *schema.Snapshot) (Snapshot, *answering) {
	t.Helper()

	session := &answering{
		fakeConn: &fakeConn{caps: mustSet(t, "fake", declaring("fake", native))},
		live:     live,
	}

	return Snapshot{Driver: newFake("fake"), Conn: session, Prefix: "migr_snap"}, session
}

// reported returns what the suite complains about when a driver answers one staged snapshot.
func reported(t *testing.T, live *schema.Snapshot) []string {
	t.Helper()

	suite, _ := snapshotSuite(t, live)

	if live == nil {
		live = &schema.Snapshot{}
	}

	return suite.differences(t, live)
}

func TestTheSuiteFailsWhenTheSnapshotIsNotTheSchemaTheFixtureBuilt(t *testing.T) {
	if len(reported(t, nil)) == 0 {
		t.Error("a session reporting an empty database passed the suite")
		t.Log("The fixture creates two tables, so a snapshot holding neither is the plainest")
		t.Log("failure this suite exists to report.")
	}
}

func TestTheSuiteFailsWhenAnElementIsMarkedBeyondTheOperationSet(t *testing.T) {
	suite, _ := snapshotSuite(t, nil)

	marked := suite.expected(t)
	marked.Tables[0].Beyond = &schema.Unrepresented{Detail: "something no operation can express"}

	problems := reported(t, &marked)

	if !strings.Contains(joined(problems), "marked table") {
		t.Errorf("the problems %v do not say the element was marked", problems)
		t.Log("The fixture is built from the operations alone, so a driver marking any of it is")
		t.Log("reporting something those operations cannot have created -- and saying so is not")
		t.Log("the same as reporting the inequality a marker also produces.")
	}
}

func TestTheSuitePassesOverTheSnapshotItsOwnFixtureDescribes(t *testing.T) {
	suite, session := snapshotSuite(t, nil)

	expected := suite.expected(t)
	session.live = &expected

	suite.Run(t)

	if session.ran == 0 {
		t.Error("the suite compared a snapshot without applying the fixture first")
	}
}

func TestTheSuiteIgnoresWhatSomeOtherTestLeftBehind(t *testing.T) {
	suite, session := snapshotSuite(t, nil)

	expected := suite.expected(t)
	expected.Tables = append(expected.Tables, schema.Table{
		Name:    "someone_elses_table",
		Columns: []schema.Column{{Name: "id", Type: logical(t, "int64")}},
	})
	session.live = &expected

	suite.Run(t)

	t.Log("One of the three engines gives every test in a package the same database, so a table")
	t.Log("another case left behind must not fail this suite -- and an element under this suite's")
	t.Log("own prefix that it does not expect still does, which the case above is.")
}

func TestTheSuiteRefusesToRunWithoutWhatItNeeds(t *testing.T) {
	for _, one := range []struct {
		name  string
		suite Snapshot
		says  string
	}{
		{"no driver", Snapshot{Conn: &answering{}, Prefix: "p"}, "no driver"},
		{"no session", Snapshot{Driver: newFake("fake"), Prefix: "p"}, "no session"},
		{"no prefix", Snapshot{Driver: newFake("fake"), Conn: &answering{}}, "no prefix"},
	} {
		t.Run(one.name, func(t *testing.T) {
			problems := one.suite.checkSuite()

			if len(problems) == 0 {
				t.Fatalf("a suite with %s reported nothing", one.name)
			}

			if !strings.Contains(joined(problems), one.says) {
				t.Errorf("the problems %v do not say %q", problems, one.says)
			}
		})
	}

	t.Log("The prefix is what makes the fixture's names unique on a server several runs share, so")
	t.Log("a suite without one would collide with the previous run rather than with nothing.")
}

func TestEveryLogicalTypeIsCarriedOrAccountedFor(t *testing.T) {
	suite, _ := snapshotSuite(t, nil)

	carried := make(map[schema.Kind]bool)
	for _, column := range suite.childColumns(t) {
		carried[column.Type.Kind()] = true
	}

	for _, kind := range schema.Kinds() {
		_, stated := inexactTypes[kind]

		if carried[kind] && stated {
			t.Errorf("%s is both in the fixture and listed as one no engine resolves alike", kind)
		}

		if !carried[kind] && !stated {
			t.Errorf("%s is neither in the fixture nor accounted for", kind)
		}
	}

	t.Log("Derived from schema.Kinds() rather than counted, so a nineteenth logical type fails")
	t.Log("this until somebody decides which half it is in.")
}
