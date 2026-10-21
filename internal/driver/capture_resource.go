package driver

import "context"

// CaptureResource is one server-side resource a change stream reads through, such as a replication slot.
type CaptureResource struct {
	// Name is the resource's name, which a shadow-table migration derives from its table and version.
	Name string
	// Active reports a resource some connection is reading through right now.
	Active bool
	// Retained is how many bytes of log the server keeps for it, which an unread resource grows without bound.
	Retained int64
	// Status is the server's own word for how that log is kept, empty where it has none.
	Status string
}

// CaptureKeeper is a session that can list and release the change-stream resources its server holds for migr.
type CaptureKeeper interface {
	// CaptureResources returns every resource migr's derived names cover on this database.
	CaptureResources(ctx context.Context) ([]CaptureResource, error)
	// ReleaseCapture drops one resource by name, and is nothing where it is already gone.
	ReleaseCapture(ctx context.Context, name string) error
}
