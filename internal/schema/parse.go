package schema

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ParseError is a type string that could not be read, located within its input.
type ParseError struct {
	Input   string
	Offset  int
	Message string
	Hint    string
}

// Error renders the failure as one line naming the input and the problem.
func (e *ParseError) Error() string {
	if e.Hint == "" {
		return fmt.Sprintf("invalid type %q: %s", e.Input, e.Message)
	}

	return fmt.Sprintf("invalid type %q: %s (%s)", e.Input, e.Message, e.Hint)
}

// Parse reads a canonical type string such as "decimal(10,2)" or "array(int32)".
func Parse(s string) (Type, error) {
	return parseAt(s, 0)
}

// parseAt parses one type, tracking array nesting so hostile input cannot recurse away.
func parseAt(s string, depth int) (Type, error) {
	input := strings.TrimSpace(s)

	if depth > maxArrayDepth {
		return Type{}, newParseError(input, 0,
			fmt.Sprintf("array nesting exceeds the limit of %d", maxArrayDepth))
	}

	if input == "" {
		return Type{}, newParseError(input, 0, "type is empty")
	}

	if err := checkBalanced(input); err != nil {
		return Type{}, err
	}

	name, args, err := split(input)
	if err != nil {
		return Type{}, err
	}

	return build(input, name, args, depth)
}

// build dispatches a parsed name and argument list to the matching constructor.
func build(input, name string, args []string, depth int) (Type, error) {
	kind, err := kindOf(input, name)
	if err != nil {
		return Type{}, err
	}

	switch kind {
	case KindString:
		return buildString(input, args)
	case KindDecimal:
		return buildDecimal(input, args)
	case KindEnum:
		return buildEnum(input, args)
	case KindArray:
		return buildArray(input, args, depth)
	case KindText, KindBool, KindInt16, KindInt32, KindInt64, KindFloat32, KindFloat64,
		KindUUID, KindJSON, KindTimestamp, KindTimestampTZ, KindDate, KindTime, KindBinary:
		return buildScalar(input, kind, args)
	default:
		return Type{}, newParseError(input, 0, fmt.Sprintf("unknown type %q", name))
	}
}

// kindOf resolves a type name, suggesting the canonical spelling when the case is wrong.
func kindOf(input, name string) (Kind, error) {
	if name == "" {
		return "", newParseError(input, 0, "type name is missing")
	}

	kind := Kind(name)
	if kind.Valid() {
		return kind, nil
	}

	err := newParseError(input, 0, fmt.Sprintf("unknown type %q", name))

	if lower := Kind(strings.ToLower(name)); lower.Valid() {
		return "", err.withHint("type names are lowercase; did you mean %q?", string(lower))
	}

	return "", err.withHint("expected one of: %s", joinKinds())
}

// buildScalar rejects arguments on a kind whose canonical form has none.
func buildScalar(input string, kind Kind, args []string) (Type, error) {
	if args != nil {
		return Type{}, newParseError(input, 0, fmt.Sprintf("%s takes no parameters", kind))
	}

	t, err := Scalar(kind)

	return fromConstructor(input, t, err)
}

// buildString reads the single length argument of a string type.
func buildString(input string, args []string) (Type, error) {
	if len(args) == 0 {
		return Type{}, newParseError(input, 0, "string requires a length").
			withHint(`try "string(255)"`)
	}

	if len(args) != 1 {
		return Type{}, newParseError(input, 0,
			fmt.Sprintf("string takes one length, got %d arguments", len(args)))
	}

	length, err := argInt(input, args[0], "length")
	if err != nil {
		return Type{}, err
	}

	t, err := String(length)

	return fromConstructor(input, t, err)
}

// buildDecimal reads the precision and scale arguments of a decimal type.
func buildDecimal(input string, args []string) (Type, error) {
	if len(args) == 0 {
		return Type{}, newParseError(input, 0, "decimal requires a precision and a scale").
			withHint(`try "decimal(10,2)"`)
	}

	if len(args) != 2 {
		return Type{}, newParseError(input, 0,
			fmt.Sprintf("decimal takes a precision and a scale, got %d", len(args))).
			withHint(`try "decimal(10,2)"`)
	}

	precision, err := argInt(input, args[0], "precision")
	if err != nil {
		return Type{}, err
	}

	scale, err := argInt(input, args[1], "scale")
	if err != nil {
		return Type{}, err
	}

	t, err := Decimal(precision, scale)

	return fromConstructor(input, t, err)
}

// buildEnum reads the value list of an enum type, whose order is significant.
func buildEnum(input string, args []string) (Type, error) {
	if len(args) == 0 {
		return Type{}, newParseError(input, 0, "enum requires at least one value").
			withHint(`try "enum(draft,live)"`)
	}

	values := make([]string, len(args))
	for i, a := range args {
		values[i] = strings.TrimSpace(a)
	}

	t, err := Enum(values...)

	return fromConstructor(input, t, err)
}

// buildArray reads the element type of an array, which is itself a full type.
func buildArray(input string, args []string, depth int) (Type, error) {
	if len(args) == 0 {
		return Type{}, newParseError(input, 0, "array requires an element type").
			withHint(`try "array(int32)"`)
	}

	if len(args) != 1 {
		return Type{}, newParseError(input, 0,
			fmt.Sprintf("array takes one element type, got %d", len(args)))
	}

	elem, err := parseAt(args[0], depth+1)
	if err != nil {
		return Type{}, newParseError(input, 0, "array element: "+innerMessage(err))
	}

	t, err := Array(elem)

	return fromConstructor(input, t, err)
}

// fromConstructor turns a constructor's validation failure into a parse error.
func fromConstructor(input string, t Type, err error) (Type, error) {
	if err != nil {
		return Type{}, newParseError(input, 0, err.Error())
	}

	return t, nil
}

// argInt reads one integer argument, naming the parameter when it is unreadable.
func argInt(input, arg, name string) (int, error) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return 0, newParseError(input, 0, name+" is missing")
	}

	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, newParseError(input, 0,
			fmt.Sprintf("%s must be an integer, got %q", name, trimmed))
	}

	return n, nil
}

// split separates a type name from its parenthesized argument list.
func split(input string) (string, []string, error) {
	open := strings.IndexByte(input, '(')
	if open < 0 {
		return input, nil, nil
	}

	if !strings.HasSuffix(input, ")") {
		return "", nil, newParseError(input, len(input), "missing closing parenthesis")
	}

	name := strings.TrimSpace(input[:open])
	inner := input[open+1 : len(input)-1]

	if strings.TrimSpace(inner) == "" {
		return name, []string{}, nil
	}

	return name, splitArgs(inner), nil
}

// splitArgs divides an argument list on commas that sit outside nested parentheses.
func splitArgs(inner string) []string {
	var (
		args  []string
		depth int
		start int
	)

	for i := range len(inner) {
		switch inner[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, inner[start:i])
				start = i + 1
			}
		}
	}

	return append(args, inner[start:])
}

// checkBalanced reports the first parenthesis that opens without closing, or closes early.
func checkBalanced(input string) error {
	depth := 0

	for i := range len(input) {
		switch input[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return newParseError(input, i, "unexpected closing parenthesis")
			}
		}
	}

	if depth > 0 {
		return newParseError(input, len(input), "missing closing parenthesis")
	}

	return nil
}

// innerMessage unwraps a nested parse failure to its message, dropping the outer quoting.
func innerMessage(err error) string {
	var pe *ParseError
	if !errors.As(err, &pe) {
		return err.Error()
	}

	if pe.Hint == "" {
		return pe.Message
	}

	return pe.Message + " (" + pe.Hint + ")"
}

// newParseError builds a positioned parse failure.
func newParseError(input string, offset int, message string) *ParseError {
	return &ParseError{Input: input, Offset: offset, Message: message}
}

// withHint attaches a suggested fix and returns the error for chaining.
func (e *ParseError) withHint(format string, args ...any) *ParseError {
	e.Hint = fmt.Sprintf(format, args...)

	return e
}
