package migr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ParseType reads a canonical type string such as "decimal(10,2)" or "array(int32)".
func ParseType(s string) Type {
	return parseTypeAt(s, 0)
}

// parseTypeAt parses one type, tracking nesting so hostile input cannot recurse away.
func parseTypeAt(s string, nesting int) Type {
	input := strings.TrimSpace(s)

	if nesting > maxArrayDepth {
		return badType(fmt.Errorf("array nesting exceeds the limit of %d", maxArrayDepth))
	}

	if input == "" {
		return badType(errors.New("type is empty"))
	}

	name, args, err := splitType(input)
	if err != nil {
		return badType(err)
	}

	return buildType(name, args, nesting)
}

// buildType dispatches a parsed name and argument list to the matching constructor.
func buildType(name string, args []string, nesting int) Type {
	kind := TypeKind(name)

	if !kind.Valid() {
		return badType(fmt.Errorf("unknown type %q (expected one of: %s)", name, joinTypeKinds()))
	}

	switch kind {
	case TypeString:
		return buildString(args)
	case TypeDecimal:
		return buildDecimal(args)
	case TypeEnum:
		return Enum(args...)
	case TypeArray:
		return buildArray(args, nesting)
	default:
		return buildScalar(kind, args)
	}
}

// buildScalar builds a parameterless type, refusing arguments it does not take.
func buildScalar(kind TypeKind, args []string) Type {
	if len(args) > 0 {
		return badType(fmt.Errorf("%s takes no parameters, got %d", kind, len(args)))
	}

	return Scalar(kind)
}

// buildString builds the string type from its single length argument.
func buildString(args []string) Type {
	if len(args) != 1 {
		return badType(fmt.Errorf("string takes one length, got %d", len(args)))
	}

	length, err := typeArg(args[0], "string length")
	if err != nil {
		return badType(err)
	}

	return String(length)
}

// buildDecimal builds the decimal type from its precision and scale arguments.
func buildDecimal(args []string) Type {
	if len(args) != 2 {
		return badType(fmt.Errorf("decimal takes a precision and a scale, got %d", len(args)))
	}

	precision, err := typeArg(args[0], "decimal precision")
	if err != nil {
		return badType(err)
	}

	scale, err := typeArg(args[1], "decimal scale")
	if err != nil {
		return badType(err)
	}

	return Decimal(precision, scale)
}

// buildArray builds the array type from its single element type, which is itself a full type.
func buildArray(args []string, nesting int) Type {
	if len(args) != 1 {
		return badType(fmt.Errorf("array takes one element type, got %d", len(args)))
	}

	return Array(parseTypeAt(args[0], nesting+1))
}

// typeArg reads a numeric type parameter, naming which one it is when it is not a number.
func typeArg(arg, name string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", name, strings.TrimSpace(arg))
	}

	return n, nil
}

// splitType separates a type's name from its argument list, refusing unbalanced input.
func splitType(input string) (string, []string, error) {
	open := strings.IndexByte(input, '(')

	if open < 0 {
		if strings.ContainsRune(input, ')') {
			return "", nil, fmt.Errorf("type %q closes a parameter list it never opened", input)
		}

		return input, nil, nil
	}

	if !strings.HasSuffix(input, ")") {
		return "", nil, fmt.Errorf("type %q opens a parameter list it never closes", input)
	}

	name := strings.TrimSpace(input[:open])
	if name == "" {
		return "", nil, fmt.Errorf("type %q names no type before its parameters", input)
	}

	inner := input[open+1 : len(input)-1]
	if err := checkBalanced(input, inner); err != nil {
		return "", nil, err
	}

	return name, splitTypeArgs(inner), nil
}

// checkBalanced reports a parameter list whose own parentheses do not match up.
func checkBalanced(input, inner string) error {
	nesting := 0

	for _, r := range inner {
		switch r {
		case '(':
			nesting++
		case ')':
			nesting--
		}

		if nesting < 0 {
			return fmt.Errorf("type %q closes a parameter list it never opened", input)
		}
	}

	if nesting != 0 {
		return fmt.Errorf("type %q opens a parameter list it never closes", input)
	}

	return nil
}

// splitTypeArgs splits an argument list on its top-level commas, leaving nested types whole.
func splitTypeArgs(inner string) []string {
	if strings.TrimSpace(inner) == "" {
		return nil
	}

	var (
		args    []string
		current strings.Builder
		nesting int
	)

	for _, r := range inner {
		switch {
		case r == '(':
			nesting++
		case r == ')':
			nesting--
		case r == ',' && nesting == 0:
			args = append(args, strings.TrimSpace(current.String()))
			current.Reset()

			continue
		}

		current.WriteRune(r)
	}

	return append(args, strings.TrimSpace(current.String()))
}
