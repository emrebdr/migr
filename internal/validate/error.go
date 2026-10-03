package validate

import "errors"

// ErrNoSource marks a validation run given nothing to ask a migration what it does.
var ErrNoSource = errors.New("a source is required: nothing else can say what a migration does")

// ErrUnbuiltCapabilities marks a declaration no driver built, which would refuse every operation.
//
// A zero capability.Set answers "an unbuilt capability set declares no capability for X" for
// everything, so a caller that filled the field with one would turn this check into twenty
// refusals per migration. Nil means the run opened no database and the check does not run.
var ErrUnbuiltCapabilities = errors.New("the capability declaration given here was never built")

// ErrUnbuiltRules marks a rule set nothing built, which would run no rule while appearing to.
//
// A zero rule.Set holds no rule, so it judges nothing and reports nothing -- which is also what a
// project that switched every rule off resolves to, and the two must not be the same value. Nil
// means the caller wants no lint pass; a set built over the configured rules, even if every one of
// them is off, is not zero.
var ErrUnbuiltRules = errors.New("the lint rules given here were never built")

// ErrNotCheckable marks a half this run could not judge at all, which is not a half that passed.
//
// A source raises it for a migration whose answer cannot be trusted -- one reaching a database a
// check deliberately did not open. It is reported rather than counted either way (ADR 0040).
var ErrNotCheckable = errors.New("this half could not be checked")
