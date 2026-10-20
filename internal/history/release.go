package history

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/ir"
)

// releaseSuffix is what a history table's name gains to name the table its releases live in.
const releaseSuffix = "_releases"

// releaseLimit is the longest release name the table stores, which is the width of its column.
const releaseLimit = 255

// Release is the deploy one batch ran under, recorded in the transaction of that batch's first applied row.
type Release struct {
	// Batch is the invocation the release names.
	Batch int64
	// Name is the release the operator gave that invocation, such as a build or a tag.
	Name string
	// RecordedAt is when it was recorded, supplied by the caller since this package reads no clock.
	RecordedAt time.Time
}

// ValidateRelease reports a release name that is blank or longer than the table stores.
func ValidateRelease(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("a release names nothing")
	case len(name) > releaseLimit:
		return fmt.Errorf("the release %q is %d bytes, and at most %d are recorded", name, len(name),
			releaseLimit)
	}

	return nil
}

// Validate reports a release that names no batch or nothing at all.
func (r Release) Validate() error {
	if r.Batch < 1 {
		return fmt.Errorf("the release %q names batch %d, and batches count from one", r.Name, r.Batch)
	}

	return ValidateRelease(r.Name)
}

// Releaser is a store that remembers which deploy each batch ran under.
type Releaser interface {
	// RecordRelease records the release of one batch, inside the open transaction.
	RecordRelease(ctx context.Context, r Release) error
	// Releases returns every release recorded, and none where the table does not exist yet.
	Releases(ctx context.Context) ([]Release, error)
}

// ReleaseTable names the table a history table's releases live in.
func ReleaseTable(table string) string {
	return table + releaseSuffix
}

// ReleaseSchema returns the operations that create the release table beside a history table.
func ReleaseSchema(table string) ir.Operations {
	return ir.Operations{
		ir.CreateTable{
			Table:      ReleaseTable(table),
			Columns:    releaseColumns(),
			PrimaryKey: []string{"batch"},
		},
	}
}

// releaseColumns returns one column per Release field, in the order the release declares them.
func releaseColumns() []ir.Column {
	return []ir.Column{
		{Name: "batch", Type: typeOf("int64")},
		{Name: "release", Type: typeOf(fmt.Sprintf("string(%d)", releaseLimit))},
		{Name: "recorded_at", Type: typeOf("timestamptz")},
	}
}

// ReleaseOf returns the release one batch ran under, and whether it ran under one.
func ReleaseOf(releases []Release, batch int64) (string, bool) {
	for _, r := range releases {
		if r.Batch == batch {
			return r.Name, true
		}
	}

	return "", false
}
