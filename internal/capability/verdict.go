package capability

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrInvalidVerdict reports a verdict no constructor in this package produced.
var ErrInvalidVerdict = errors.New("zero Verdict is not a capability verdict")

// Verdict is one driver's answer for one operation, on both axes at once.
type Verdict struct {
	support  Support
	strategy string
	reason   string
	lock     LockImpact
	// lowest is what the operation costs where condition holds, which only a session can confirm.
	lowest LockImpact
	// condition says what has to hold of the table for the operation to cost only lowest.
	condition string
}

// Native returns the verdict for an operation the engine performs directly.
func Native(lock LockImpact) Verdict {
	return Verdict{support: SupportNative, lock: lock}
}

// Emulated returns the verdict for an operation achieved another way, naming the strategy.
func Emulated(strategy string, lock LockImpact) Verdict {
	if strings.TrimSpace(strategy) == "" {
		return Unsupported("an emulation was declared without naming the strategy it uses")
	}

	return Verdict{support: SupportEmulated, strategy: strategy, lock: lock}
}

// Unsupported returns the verdict for an operation the driver refuses, and why.
func Unsupported(reason string) Verdict {
	if strings.TrimSpace(reason) == "" {
		reason = "a refusal was declared without giving a reason"
	}

	return Verdict{support: SupportUnsupported, reason: reason, lock: LockUnknown}
}

// Unless returns the verdict lowered to a cheaper impact where a stated condition holds of the table.
func (v Verdict) Unless(lower LockImpact, condition string) Verdict {
	if !v.Allowed() || strings.TrimSpace(condition) == "" || !slices.Contains(ceilings, lower) ||
		lower.severity() >= v.lock.severity() {
		return v
	}

	v.lowest, v.condition = lower, condition

	return v
}

// Support returns which of the three verdicts this is.
func (v Verdict) Support() Support {
	return v.support
}

// Strategy returns how an emulated operation is achieved, and nothing for any other verdict.
func (v Verdict) Strategy() string {
	return v.strategy
}

// Reason returns why an operation was refused, and nothing for any other verdict.
func (v Verdict) Reason() string {
	return v.reason
}

// Lock returns what the operation costs a live database.
func (v Verdict) Lock() LockImpact {
	return v.lock
}

// Confirmed returns the verdict at its cheaper cost, for a session that has seen its condition hold.
func (v Verdict) Confirmed() Verdict {
	if v.condition == "" {
		return v
	}

	v.lock, v.lowest, v.condition = v.lowest, "", ""

	return v
}

// Lowest returns what the operation costs where its condition holds, or Lock when it carries none.
func (v Verdict) Lowest() LockImpact {
	if v.condition == "" {
		return v.lock
	}

	return v.lowest
}

// Condition returns what has to hold of the table for the operation to cost only Lowest.
func (v Verdict) Condition() string {
	return v.condition
}

// Allowed reports whether the driver will perform the operation, natively or by emulation.
func (v Verdict) Allowed() bool {
	return v.support == SupportNative || v.support == SupportEmulated
}

// String renders a verdict for plan output, naming the strategy or the reason.
func (v Verdict) String() string {
	switch v.support {
	case SupportNative, SupportEmulated:
		return fmt.Sprintf("%s%s (%s%s)", v.support, v.describeStrategy(), v.lock,
			v.describeCondition())
	case SupportUnsupported:
		return fmt.Sprintf("%s: %s", v.support, v.reason)
	default:
		return "invalid verdict"
	}
}

// describeStrategy renders an emulation's named strategy, or nothing when there is none.
func (v Verdict) describeStrategy() string {
	if v.strategy == "" {
		return ""
	}

	return ": " + v.strategy
}

// describeCondition renders the cheaper impact a condition allows, or nothing when there is none.
func (v Verdict) describeCondition() string {
	if v.condition == "" {
		return ""
	}

	return fmt.Sprintf("; %s where %s", v.lowest, v.condition)
}

// wireVerdict is the JSON shape of a verdict, whose own fields are deliberately unexported.
type wireVerdict struct {
	Support  Support    `json:"support"`
	Strategy string     `json:"strategy,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Lock     LockImpact `json:"lock_impact"`
}

// MarshalJSON renders a verdict for host.capability.get, refusing one no constructor built.
func (v Verdict) MarshalJSON() ([]byte, error) {
	if !v.support.Valid() || !v.lock.Valid() {
		return nil, ErrInvalidVerdict
	}

	return json.Marshal(wireVerdict{
		Support:  v.support,
		Strategy: v.strategy,
		Reason:   v.reason,
		Lock:     v.lock,
	})
}
