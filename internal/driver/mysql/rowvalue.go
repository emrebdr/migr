package mysql

import (
	"fmt"
	"strconv"
	"strings"
)

// A captured value is rendered into the text the server would answer with, and bound back by its class.
//
// Each value arrives decoded by the binlog reader as a Go value of the column's wire type, which is not
// the text a read returns: an unsigned integer arrives signed, an enum as its index and a set as its
// bitmask, a float as a float32, and a timestamp in whatever zone the reader was told -- this one, UTC.
// Rendering undoes each, so a replayed value is the value the copy would have read. A float is rendered
// as the exact double its float32 is, because a float column widened to a double converts the binary
// value, and the shortest text of the float32 would convert to a different double. Text and binary
// values carry their bytes unchanged; text is bound through CONVERT(... USING <its charset>), so those
// bytes are read in the character set they were written in whatever the session's is.

// render returns a decoded binlog value as the text the server would answer with, nil for NULL.
func render(column sourceColumn, value any) (*string, error) {
	if value == nil {
		return nil, nil
	}

	text, err := renderValue(column, value)
	if err != nil {
		return nil, fmt.Errorf("column %s (%s): %w", column.name, column.data, err)
	}

	return &text, nil
}

// renderValue renders one non-NULL value by its column's class.
func renderValue(column sourceColumn, value any) (string, error) {
	switch column.class {
	case classInteger:
		return renderInteger(column, value)
	case classFloat:
		return renderFloat(value)
	case classEnum:
		return renderEnum(column, value)
	case classSet:
		return renderSet(column, value)
	case classBit:
		return renderBit(value)
	case classNumber, classText, classJSON, classBinary, classTemporal:
		return renderPlain(value)
	default:
		return "", fmt.Errorf("a %s value is not one a shadow table replays", column.data)
	}
}

// renderInteger renders an integer, undoing the sign the log gives an unsigned column's value.
func renderInteger(column sourceColumn, value any) (string, error) {
	signed, ok := asInt64(value)
	if !ok {
		return "", fmt.Errorf("an integer arrived as %T", value)
	}

	if !column.unsigned {
		return strconv.FormatInt(signed, 10), nil
	}

	bits := integerBits[column.data]
	if bits == 0 || bits == 64 {
		return strconv.FormatUint(bitsOf(signed), 10), nil
	}

	return strconv.FormatUint(bitsOf(signed)&(1<<bits-1), 10), nil
}

// asInt64 widens every integer type the reader decodes to.
func asInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// bitsOf reads a signed integer's bits as unsigned, which is how the log carries an unsigned, bit or set value.
func bitsOf(signed int64) uint64 {
	return uint64(signed) //nolint:gosec // G115: the reinterpretation is the point -- the log stores these bits signed
}

// renderFloat renders a float column's value as the exact double it is.
func renderFloat(value any) (string, error) {
	switch v := value.(type) {
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 64), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("a float arrived as %T", value)
	}
}

// renderEnum renders an enum's index as its label, the empty string being the error value MySQL keeps as 0.
func renderEnum(column sourceColumn, value any) (string, error) {
	index, ok := asInt64(value)
	if !ok {
		return "", fmt.Errorf("an enum arrived as %T", value)
	}

	switch {
	case index == 0:
		return "", nil
	case index < 0 || int(index) > len(column.labels):
		return "", fmt.Errorf("index %d names none of the %d members", index, len(column.labels))
	default:
		return column.labels[index-1], nil
	}
}

// renderSet renders a set's bitmask as its members, comma-joined in declared order.
func renderSet(column sourceColumn, value any) (string, error) {
	mask, ok := asInt64(value)
	if !ok {
		return "", fmt.Errorf("a set arrived as %T", value)
	}

	var members []string

	for i, label := range column.labels {
		if bitsOf(mask)&(1<<uint(i)) != 0 {
			members = append(members, label)
		}
	}

	if remaining := bitsOf(mask) >> uint(len(column.labels)); remaining != 0 {
		return "", fmt.Errorf("bitmask %d names a member past the %d declared", mask, len(column.labels))
	}

	return strings.Join(members, ","), nil
}

// renderBit renders a bit column's value as the number it is.
func renderBit(value any) (string, error) {
	bits, ok := asInt64(value)
	if !ok {
		return "", fmt.Errorf("a bit value arrived as %T", value)
	}

	return strconv.FormatUint(bitsOf(bits), 10), nil
}

// renderPlain renders a value the reader already decoded as text or bytes, or as a number for year.
func renderPlain(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		if n, ok := asInt64(value); ok {
			return strconv.FormatInt(n, 10), nil
		}

		return "", fmt.Errorf("a value arrived as %T", value)
	}
}

// bind returns the placeholder a captured value takes in a statement and the argument it binds.
func bind(column sourceColumn, value *string) (string, any, error) {
	if value == nil {
		return "?", nil, nil
	}

	switch column.class {
	case classText, classEnum, classSet:
		return "CONVERT(? USING " + charsetOf(column) + ")", []byte(*value), nil
	case classJSON:
		return "CONVERT(? USING utf8mb4)", []byte(*value), nil
	case classBinary:
		return "?", []byte(*value), nil
	case classBit:
		bits, err := strconv.ParseUint(*value, 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("column %s: a bit value %q is not a number: %w", column.name, *value, err)
		}

		return "?", bits, nil
	default:
		return "?", *value, nil
	}
}

// charsetOf names the character set a text column's bytes were written in, which is never spliced unchecked.
func charsetOf(column sourceColumn) string {
	for _, ch := range column.charset {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
			return characterSet
		}
	}

	if column.charset == "" {
		return characterSet
	}

	return column.charset
}
