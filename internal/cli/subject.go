package cli

import "fmt"

// subject is what a report is about, so one renderer serves the migration history and the seed one.
type subject struct {
	// command prefixes a report's heading, empty for the migration commands.
	command string
	// noun names one of what the report lists.
	noun string
	// table names the history the report reads.
	table string
	// creator is the command that would create that table.
	creator string
}

// migrationReports is what a report is about unless it says otherwise, so the zero subject means it.
var migrationReports = subject{noun: "migration", table: "migration history table", creator: "migr up"}

// seedReports is what a `migr seed` subcommand reports on.
var seedReports = subject{command: "seed ", noun: "seed", table: "seed history table",
	creator: "migr seed"}

// or returns this subject, or migrationReports where it was never set.
func (s subject) or() subject {
	if s.noun == "" {
		return migrationReports
	}

	return s
}

// count counts what the report lists with the right noun.
func (s subject) count(n int) string {
	if n == 1 {
		return "1 " + s.noun
	}

	return fmt.Sprintf("%d %ss", n, s.noun)
}

// files counts the files on disk with the verb and noun that agree with the count.
func (s subject) files(n int) string {
	if n == 1 {
		return "there is 1 " + s.noun + " file"
	}

	return fmt.Sprintf("there are %d %s files", n, s.noun)
}
