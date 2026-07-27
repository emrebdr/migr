// Command commentlint enforces the repository's two comment rules over every Go file
// below the given paths, defaulting to the working directory.
//
// It exits 0 when clean, 1 when it finds violations, and 2 when a path cannot be walked
// or a file cannot be parsed. A file that fails to parse is an error rather than a silent
// skip: a check that quietly ignores what it cannot read reports clean for the wrong
// reason.
//
// See internal/lint for the rules, the exemptions, and why this walks the filesystem
// rather than running as a go/analysis pass.
package main

import (
	"fmt"
	"os"

	"github.com/emrebdr/migr/internal/lint"
)

const (
	exitViolations = 1
	exitError      = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run checks the given paths, reports findings, and returns the process exit code.
func run(paths []string, out *os.File) int {
	if len(paths) == 0 {
		paths = []string{"."}
	}

	findings, err := lint.Check(paths...)
	if err != nil {
		fmt.Fprintf(out, "commentlint: %v\n", err)

		return exitError
	}

	if len(findings) == 0 {
		return 0
	}

	return report(findings, out)
}

// report prints every finding and returns the violation exit code.
func report(findings []lint.Finding, out *os.File) int {
	for _, f := range findings {
		fmt.Fprintln(out, f)
	}

	fmt.Fprintf(out, "commentlint: %d violation(s)\n", len(findings))

	return exitViolations
}
