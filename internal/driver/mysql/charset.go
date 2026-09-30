package mysql

import "github.com/emrebdr/migr/internal/schema"

// characterSet is the one this driver declares on every column that can carry one (doc.go).
const characterSet = "utf8mb4"

// collation is the one this driver declares, so a comparison means the same thing on every engine.
const collation = "utf8mb4_bin"

// bytesPerCharacter is what utf8mb4 costs at its widest, and every length limit is counted in it.
const bytesPerCharacter = 4

// maxRowBytes is InnoDB's row limit, which bounds one varchar as well as the whole row.
const maxRowBytes = 65535

// maxColumnCharacters is how wide a varchar this driver will declare, which the server states itself.
//
// Error 1074 names the number: "Column length too big for column 'c' (max = 16383)", measured on
// both servers, and varchar(16383) is accepted where varchar(20000) is not. It is 65535/4 because
// the character set is pinned rather than inherited, which is what makes the bound decidable here.
const maxColumnCharacters = maxRowBytes / bytesPerCharacter

// maxKeyBytes is the widest key InnoDB indexes, error 1071 on both servers past it.
const maxKeyBytes = 3072

// collated reports a logical type MySQL lets a character set be declared on.
func collated(t schema.Type) bool {
	switch t.Kind() {
	case schema.KindString, schema.KindText, schema.KindUUID, schema.KindEnum:
		return true
	case schema.KindBool, schema.KindInt16, schema.KindInt32, schema.KindInt64, schema.KindFloat32,
		schema.KindFloat64, schema.KindDecimal, schema.KindJSON, schema.KindTimestamp,
		schema.KindTimestampTZ, schema.KindDate, schema.KindTime, schema.KindBinary,
		schema.KindArray:
		return false
	}

	return false
}

// collateClause renders the character set and collation this driver pins, stated in one place.
func collateClause() string {
	return "CHARACTER SET " + characterSet + " COLLATE " + collation
}

// characterSetClause renders the character set a column declares, and nothing where it may declare none.
func characterSetClause(t schema.Type) string {
	if !collated(t) {
		return ""
	}

	return " " + collateClause()
}

// tableOptions renders the character set a table declares for whatever is added to it later.
func tableOptions() string {
	return " DEFAULT " + collateClause()
}

// keyBytes reports how many bytes a column of this type contributes to an index key.
func keyBytes(t schema.Type) int {
	length, isString := t.Length()
	if isString {
		return length * bytesPerCharacter
	}

	if t.Kind() == schema.KindUUID {
		return uuidCharacters * bytesPerCharacter
	}

	return 0
}

// indexable reports whether MySQL will put a column of this type in a key without a prefix length.
func indexable(t schema.Type) bool {
	switch t.Kind() {
	case schema.KindText, schema.KindBinary:
		return false
	case schema.KindString, schema.KindBool, schema.KindInt16, schema.KindInt32, schema.KindInt64,
		schema.KindFloat32, schema.KindFloat64, schema.KindDecimal, schema.KindUUID,
		schema.KindJSON, schema.KindTimestamp, schema.KindTimestampTZ, schema.KindDate,
		schema.KindTime, schema.KindEnum, schema.KindArray:
		return true
	}

	return true
}
