package host

import "errors"

// ErrNoConn marks a server built without the session every database callback reaches.
var ErrNoConn = errors.New("a host callback server needs an open session")

// ErrNoDatabase marks a server built without the name host.info reports.
var ErrNoDatabase = errors.New("a host callback server needs the name migr.yaml holds the target under")

// ErrNoLog marks a server built without somewhere to file host.log's lines.
var ErrNoLog = errors.New("a host callback server needs a logger for host.log")

// ErrNoMode marks a call opened without saying whether it runs the migration or asks it a question.
var ErrNoMode = errors.New("a migration.execute states the mode it is asked in")

// ErrNoMigration marks a call opened for no migration in particular.
var ErrNoMigration = errors.New("a call names the migration it is in flight for")

// ErrCallOpen marks a second call opened while one is still in flight, which the engine never does.
var ErrCallOpen = errors.New("a call is already in flight, and the engine has at most one")

// ErrNothingRecorded marks operations asked of a window that records none, which validate is.
var ErrNothingRecorded = errors.New("a migration.validate window records no operations")
