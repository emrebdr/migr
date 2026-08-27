package validate

import "errors"

// ErrNoSource marks a validation run given nothing to ask a migration what it does.
var ErrNoSource = errors.New("a source is required: nothing else can say what a migration does")

// ErrNotCheckable marks a half this run could not judge at all, which is not a half that passed.
//
// A source raises it for a migration whose answer cannot be trusted -- one reaching a database a
// check deliberately did not open. It is reported rather than counted either way (ADR 0040).
var ErrNotCheckable = errors.New("this half could not be checked")
