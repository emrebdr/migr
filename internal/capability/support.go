package capability

import "slices"

// Support is whether a driver can perform an operation at all.
type Support string

// The support verdicts. The zero Support names none of them and is not a verdict.
const (
	SupportNative      Support = "native"
	SupportEmulated    Support = "emulated"
	SupportUnsupported Support = "unsupported"
)

// supports lists every support verdict, from the most direct to the least.
var supports = []Support{SupportNative, SupportEmulated, SupportUnsupported}

// Supports returns every support verdict a driver may report, as a copy.
func Supports() []Support {
	return slices.Clone(supports)
}

// Valid reports whether a value names one of the three support verdicts.
func (s Support) Valid() bool {
	return slices.Contains(supports, s)
}

// String returns the verdict's wire name.
func (s Support) String() string {
	return string(s)
}
