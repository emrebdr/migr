package diff

import (
	"fmt"

	"github.com/emrebdr/migr/internal/schema"
)

// Structure decides and the name only breaks a tie, because the name is the thing that changed. A
// renamed column keeps its type, so type equality is the floor: a pair that does not share one is a
// drop and an add, not a rename anybody would confirm. A renamed table keeps its columns, so a
// majority of them matching is the floor there.
//
// What a score is worth is never shown to anybody. It orders the questions; the evidence answers
// them, and it is prose because "same type string(255), and the only column this table lost or
// gained" is what a person can check against their own intent, where 0.83 is not.

// columnLikeness scores one lost column against one gained one, and says what makes the pair likely.
func columnLikeness(was, is schema.Column) (int, string) {
	if !was.Type.Equal(is.Type) {
		return 0, ""
	}

	score := likenessFloor
	reasons := []string{fmt.Sprintf("both are %s", is.Type)}

	if was.Nullable == is.Nullable {
		score += likenessAttribute
		reasons = append(reasons, describeNullable(is.Nullable))
	}

	if was.Default.Equal(is.Default) {
		score += likenessAttribute
		reasons = append(reasons, describeSameDefault(is.Default))
	}

	return score + nameLikeness(was.Name, is.Name), joinReasons(reasons)
}

// The score's three bands exist so that no number of weak agreements can outrank the floor: a pair
// that matches on type alone always outranks every pair that does not match on type, because the
// latter never gets a score at all.
const (
	// likenessFloor is what a pair scores for clearing the floor, and is the bulk of any score.
	likenessFloor = 1000
	// likenessAttribute is what one further agreement adds, which only ever breaks a tie.
	likenessAttribute = 10
	// likenessName is what a shared word in the two names adds, which is the weakest evidence.
	likenessName = 1
)

// describeNullable says what a matched pair agrees about accepting null, in a person's words.
func describeNullable(nullable bool) string {
	if nullable {
		return "both accept null"
	}

	return "neither accepts null"
}

// describeSameDefault says what a matched pair agrees about defaulting, an absent default included.
func describeSameDefault(value *schema.ColumnDefault) string {
	if value == nil {
		return "neither has a default"
	}

	return fmt.Sprintf("both default to %s", value)
}

// A table keeps its columns through a rename, but a refactor that renames a table usually renames
// some of its columns in the same breath -- so matching on names alone misses the case that matters
// most. Measured by hand against a plausible pair, users -> accounts with two columns renamed and one
// added scored nothing at all and the operator was handed a drop_table. So a column is accounted for
// by name or by type, and the two are counted apart because the evidence they give a person is
// different: three columns identical is near-certain, three sharing only a type is worth a look.

// tableLikeness scores one lost table against one gained one, on how much of it is the same table.
func tableLikeness(was, is schema.Table) (int, string) {
	identical, typed := accountedFor(was, is)
	if !majority(identical+typed, len(was.Columns)) {
		return 0, ""
	}

	reasons := []string{describeShared(identical, typed, len(was.Columns))}
	score := likenessFloor + identical*likenessAttribute + typed

	if keysAlike(was.PrimaryKey, is.PrimaryKey) {
		score += likenessAttribute
		reasons = append(reasons, describeKey(len(is.PrimaryKey.Columns)))
	}

	return score + nameLikeness(was.Name, is.Name), joinReasons(reasons)
}

// Only the count of the key's columns is compared, not the columns themselves: a table renamed along
// with the column it is keyed on would otherwise lose the evidence its key gives.

// keysAlike reports two primary keys covering the same number of columns, neither table lacking one.
func keysAlike(was, is *schema.PrimaryKey) bool {
	return was != nil && is != nil && len(was.Columns) == len(is.Columns)
}

// Only the table being left is asked for a majority, and the one being joined is not: a renamed table
// may gain any number of columns, so requiring most of the target to be accounted for would refuse a
// rename for the unrelated reason that the refactor also grew the table.

// majority reports whether a count is more than half of a total, an empty total having no majority.
func majority(count, total int) bool {
	return total > 0 && count*2 > total
}

// describeKey says what two tables agree about their primary keys, which is its width and not more.
func describeKey(columns int) string {
	if columns == 1 {
		return "both are keyed on a single column"
	}

	return fmt.Sprintf("both are keyed on %d columns", columns)
}

// accountedFor counts how much of one table the other holds, by name and then by type alone.
func accountedFor(was, is schema.Table) (identical, typed int) {
	claimed := make([]bool, len(is.Columns))
	unmatched := make([]schema.Column, 0, len(was.Columns))

	for _, column := range was.Columns {
		at := indexOfIdentical(is.Columns, column)
		if at < 0 {
			unmatched = append(unmatched, column)

			continue
		}

		claimed[at] = true
		identical++
	}

	for _, column := range unmatched {
		at := indexOfTyped(is.Columns, column, claimed)
		if at < 0 {
			continue
		}

		claimed[at] = true
		typed++
	}

	return identical, typed
}

// indexOfIdentical finds a column the other table holds under the same name and states the same way.
func indexOfIdentical(columns []schema.Column, column schema.Column) int {
	for i, other := range columns {
		if other.Name == column.Name && other.Equal(column) {
			return i
		}
	}

	return -1
}

// indexOfTyped finds an unclaimed column of the same type, which is a renamed column's own evidence.
func indexOfTyped(columns []schema.Column, column schema.Column, claimed []bool) int {
	for i, other := range columns {
		if !claimed[i] && other.Type.Equal(column.Type) {
			return i
		}
	}

	return -1
}

// describeShared says how much of a table the other one accounts for, and how precisely.
func describeShared(identical, typed, before int) string {
	switch {
	case typed == 0:
		return fmt.Sprintf("all %d of its columns are there unchanged", identical)
	case identical == 0:
		return fmt.Sprintf("%d of its %d columns %s one there by type but not by name",
			typed, before, agree(typed, "matches", "match"))
	default:
		return fmt.Sprintf("%d of its %d columns %s there unchanged and %d more %s one by type "+
			"but not by name", identical, before, agree(identical, "is", "are"), typed,
			agree(typed, "matches", "match"))
	}
}

// agree picks the verb form a count takes, since every one of these clauses names a number.
func agree(count int, one, many string) string {
	if count == 1 {
		return one
	}

	return many
}

// A shared word is the weakest evidence there is and is deliberately worth the least: user_id and
// account_id share "id", which says nothing, and created_at and updated_at share "at". It breaks a
// tie between two pairs that already agree on everything structural, and decides nothing on its own.

// nameLikeness scores two names on the words they share, which is only ever a tiebreak.
func nameLikeness(was, is string) int {
	shared := 0
	words := splitWords(is)

	for _, word := range splitWords(was) {
		for _, other := range words {
			if word == other {
				shared++

				break
			}
		}
	}

	return shared * likenessName
}

// splitWords breaks a name into the underscore-separated words §9's grammar builds one from.
func splitWords(name string) []string {
	var words []string

	word := ""

	for _, r := range name {
		if r == '_' {
			words = appendWord(words, word)
			word = ""

			continue
		}

		word += string(r)
	}

	return appendWord(words, word)
}

// appendWord adds one word to a list unless it is empty, which a doubled separator produces.
func appendWord(words []string, word string) []string {
	if word == "" {
		return words
	}

	return append(words, word)
}

// joinReasons renders the evidence for one pair as the clause a prompt prints after the names.
func joinReasons(reasons []string) string {
	if len(reasons) == 1 {
		return reasons[0]
	}

	joined := reasons[0]

	for _, reason := range reasons[1 : len(reasons)-1] {
		joined += ", " + reason
	}

	return joined + ", and " + reasons[len(reasons)-1]
}
