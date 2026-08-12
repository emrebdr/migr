package validate

import "errors"

// ErrNoSource marks a validation run given nothing to ask a migration what it does.
var ErrNoSource = errors.New("a source is required: nothing else can say what a migration does")
