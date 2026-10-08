package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/diff"
	"github.com/emrebdr/migr/internal/schema"
)

// M11-T4's done-when is that a likely rename is confirmed interactively rather than guessed, and
// this is the asking. It sits between two pure calls -- diff.Candidates proposes and diff.Diff is
// told -- because invariant #8 forbids a planning path whose output depends on anything but its
// arguments, so a confirmer handed to Diff would make the same two snapshots diff two ways.
//
// The default is no, and the asymmetry is the whole argument. A declined rename becomes a drop and a
// create, which invariant #7 gates: migr up refuses the generated migration until the project
// permits that kind. A confirmed rename is Destructive() == false and is gated by nothing at all. So
// the answer nothing else checks is the one a person has to type, and every other answer -- an empty
// line, end of input, a terminal that is not there -- declines.
//
// Its caller is M11-T5. Writing it there instead would leave this task's own done-when unmet, and
// the policy above is this task's to decide rather than that command's.

// The answers this prompt takes, which are the only three things it does anything with.
const (
	// confirmAnswer is the one answer that turns a candidate into an operation.
	confirmAnswer = "y"
	// declineAnswer is the explicit no, which is also what every unanswered prompt means.
	declineAnswer = "n"
	// stopAnswer declines this candidate and every one after it, without asking again.
	stopAnswer = "q"
	// exhaustedAnswer is what a stream with nothing left in it answers, which is nobody answering.
	exhaustedAnswer = "eof"
)

// ErrNotConfirmable reports a prompt that could not be put to anybody, which confirms nothing.
var ErrNotConfirmable = errors.New("renames cannot be confirmed without somewhere to ask")

// A confirmed table rename makes the column renames inside it detectable, so the rounds are driven
// here rather than by the caller: this is the only thing that knows which candidates it has already
// put to somebody, and a loop outside it would re-ask every one that was declined. A round that
// confirms nothing ends it, which is what makes it terminate.

// ConfirmRenames asks about every rename two schemas make likely and returns the ones confirmed.
func ConfirmRenames(in io.Reader, out io.Writer, from, to schema.Snapshot) (diff.Renames, error) {
	prompt := &renamePrompt{out: out, asked: map[string]bool{}}

	var confirmed diff.Renames

	for {
		found := prompt.unasked(diff.Candidates(from, to, confirmed))
		if len(found) == 0 {
			return confirmed, prompt.close(confirmed)
		}

		if in == nil || out == nil {
			return diff.Renames{}, ErrNotConfirmable
		}

		if prompt.reader == nil {
			prompt.reader = bufio.NewReader(in)
		}

		round, stopped, err := prompt.ask(found)
		if err != nil {
			return diff.Renames{}, err
		}

		confirmed = merged(confirmed, round)
		prompt.confirmed = confirmed

		if stopped {
			return confirmed, nil
		}

		if round.Empty() {
			return confirmed, prompt.close(confirmed)
		}
	}
}

// renamePrompt is one run of questions: where they go, who answers, and which have been put already.
type renamePrompt struct {
	// out is where the questions and the summary are written.
	out io.Writer
	// reader is what answers them, built once the first question is actually asked.
	reader *bufio.Reader
	// asked names every candidate already put to somebody, so no round repeats one.
	asked map[string]bool
	// total counts every question asked across every round, which the summary names.
	total int
	// opened reports whether anything has been written, since a prompt nobody saw says nothing.
	opened bool
	// confirmed is what the rounds before this one confirmed, which a stopped run still reports.
	confirmed diff.Renames
}

// unasked returns the candidates this prompt has not already put to somebody.
func (p *renamePrompt) unasked(found []diff.Candidate) []diff.Candidate {
	kept := make([]diff.Candidate, 0, len(found))

	for _, one := range found {
		if !p.asked[one.Describe()] {
			kept = append(kept, one)
		}
	}

	return kept
}

// ask puts one round of candidates, reporting what was confirmed and whether somebody stopped.
func (p *renamePrompt) ask(found []diff.Candidate) (diff.Renames, bool, error) {
	if err := p.open(len(found)); err != nil {
		return diff.Renames{}, false, err
	}

	var round diff.Renames

	for i, one := range found {
		p.asked[one.Describe()] = true
		p.total++

		answer, err := askRename(p.reader, p.out, p.total, one)
		if err != nil {
			return diff.Renames{}, false, err
		}

		if answer == stopAnswer || answer == exhaustedAnswer {
			return round, true, writeRenameUnasked(p.out, answer, merged(p.confirmed, round),
				len(found)-i)
		}

		if answer == confirmAnswer {
			round = round.With(one)
		}
	}

	return round, false, nil
}

// open writes the header for the first round, a later round needing no second introduction.
func (p *renamePrompt) open(pending int) error {
	if p.opened {
		return writeRenameRound(p.out, pending)
	}

	p.opened = true

	return writeRenameHeader(p.out, pending)
}

// close writes the summary, and nothing at all where there was never anything to ask.
func (p *renamePrompt) close(confirmed diff.Renames) error {
	if !p.opened {
		return nil
	}

	return writeRenameSummary(p.out, confirmed, p.total)
}

// merged returns one set of renames holding both, which is how a round joins what came before it.
func merged(held, round diff.Renames) diff.Renames {
	return diff.Renames{
		Tables:  append(slices.Clone(held.Tables), round.Tables...),
		Columns: append(slices.Clone(held.Columns), round.Columns...),
	}
}

// writeRenameHeader says what is about to be asked and what the questions are for.
func writeRenameHeader(out io.Writer, pending int) error {
	_, err := fmt.Fprintf(out, "\n%d likely %s. Each is a guess until you confirm it, because two\n"+
		"schemas record a rename and an unrelated drop the same way.\n",
		pending, plural(pending, "rename", "renames"))

	return err
}

// Each prompt states both outcomes rather than only the rename, because that is the choice being
// made: a prompt reading "rename x to y? [y/N]" hides that saying no destroys the data instead.

// askRename puts one candidate to a person and returns the answer, re-asking anything unrecognized.
func askRename(asked *bufio.Reader, out io.Writer, at int, one diff.Candidate) (string, error) {
	if err := writeCandidate(out, at, one); err != nil {
		return "", err
	}

	for {
		line, err := asked.ReadString('\n')
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
			return exhaustedAnswer, nil
		}

		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading the answer to a rename: %w", err)
		}

		answer, known := readAnswer(line)
		if known {
			return answer, nil
		}

		if written := writeRenameReask(out); written != nil {
			return "", written
		}
	}
}

// readAnswer reads one line as an answer, reporting whether it is one this prompt recognizes.
func readAnswer(line string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case confirmAnswer, "yes":
		return confirmAnswer, true
	case declineAnswer, "no", "":
		return declineAnswer, true
	case stopAnswer, "quit":
		return stopAnswer, true
	default:
		return "", false
	}
}

// writeCandidate prints one candidate, what confirming it does, and what declining it does instead.
func writeCandidate(out io.Writer, at int, one diff.Candidate) error {
	_, err := fmt.Fprintf(out, "\n  %d. %s\n     yes: %s\n      no: %s\n     rename? [y/N/q] ",
		at, one.Describe(), describeRename(one), describeReplacement(one))

	return err
}

// writeRenameRound says a confirmed rename made more of them detectable, which is why there is more.
func writeRenameRound(out io.Writer, pending int) error {
	_, err := fmt.Fprintf(out, "\n  confirming that made %d more %s likely.",
		pending, plural(pending, "rename", "renames"))

	return err
}

// describeRename says what confirming one candidate makes the migration do.
func describeRename(one diff.Candidate) string {
	if one.Column() {
		return "rename the column, keeping every row's value in it"
	}

	return "rename the table, keeping every row in it"
}

// describeReplacement says what declining one candidate makes the migration do instead.
func describeReplacement(one diff.Candidate) string {
	if one.Column() {
		return fmt.Sprintf("drop %q and add %q, which loses every value in %q",
			one.From, one.To, one.From)
	}

	return fmt.Sprintf("drop %q and create %q, which loses every row in %q",
		one.From, one.To, one.From)
}

// writeRenameReask says which answers this prompt takes, having been given one it does not.
func writeRenameReask(out io.Writer) error {
	_, err := fmt.Fprint(out, "     answer y to rename, n to drop and create instead, or q to "+
		"stop asking: ")

	return err
}

// A stream that ran out is not somebody saying no, and the two are reported apart because they are
// different facts: one is an operator stopping, the other is nobody having been there. Both decline,
// which is the half that matters -- an unread byte must never become a rename.

// writeRenameUnasked closes a run nobody finished, naming what it confirmed and what it never asked.
func writeRenameUnasked(out io.Writer, answer string, confirmed diff.Renames, pending int) error {
	reason := "stopped"
	if answer == exhaustedAnswer {
		reason = "nothing left to read, so nobody answered"
	}

	_, err := fmt.Fprintf(out, "\n  %s: %d confirmed, and %d %s left unconfirmed and will be "+
		"proposed as a drop and a create\n\n",
		reason, confirmedCount(confirmed), pending, plural(pending, "rename", "renames"))

	return err
}

// confirmedCount counts the renames one answer-collecting run has confirmed, of both kinds.
func confirmedCount(confirmed diff.Renames) int {
	return len(confirmed.Tables) + len(confirmed.Columns)
}

// writeRenameSummary reports how many of the candidates were confirmed, and what the rest become.
func writeRenameSummary(out io.Writer, confirmed diff.Renames, asked int) error {
	taken := confirmedCount(confirmed)
	if taken == asked {
		_, err := fmt.Fprintf(out, "\n  %d of %d confirmed\n\n", taken, asked)

		return err
	}

	_, err := fmt.Fprintf(out, "\n  %d of %d confirmed; the %s will be proposed as a drop and a "+
		"create\n\n", taken, asked, plural(asked-taken, "other", "others"))

	return err
}

// plural picks the singular or the plural form for a count, since every line here names one.
func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}

	return many
}
