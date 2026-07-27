package lint

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

const (
	msgInBody = "comment inside a function body; extract the block into a named function " +
		"so the name carries the explanation"
	msgLongDoc = "doc comment must be a single line; move longer prose into the package doc, " +
		"docs/, or an ADR"
)

// exemptPrefixes are the comment forms that instruct tooling rather than explain code.
var exemptPrefixes = []string{"//go:", "//nolint:", "// Deprecated:"}

// skippedDirs are directories the Go toolchain itself never treats as source.
var skippedDirs = map[string]bool{
	"testdata":     true,
	"vendor":       true,
	"node_modules": true,
	"bin":          true,
	"dist":         true,
}

// Finding is one comment-rule violation, located in a source file.
type Finding struct {
	Path    string
	Line    int
	Column  int
	Message string
}

// String renders the finding as file:line:col: message, the form editors make clickable.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s", f.Path, f.Line, f.Column, f.Message)
}

// Check walks every given path and reports the comment-rule violations found below them.
func Check(paths ...string) ([]Finding, error) {
	var findings []Finding

	for _, root := range paths {
		found, err := checkTree(root)
		if err != nil {
			return nil, err
		}

		findings = append(findings, found...)
	}

	return findings, nil
}

// CheckFile parses a single Go source file and reports its comment-rule violations.
func CheckFile(path string) ([]Finding, error) {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	findings := append(inBodyFindings(fset, file), longDocFindings(fset, file)...)
	sortFindings(findings)

	return findings, nil
}

// checkTree walks one root directory and collects findings from every Go file below it.
func checkTree(root string) ([]Finding, error) {
	var findings []Finding

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return dirAction(path, entry.Name())
		}

		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}

		found, err := CheckFile(path)
		if err != nil {
			return err
		}

		findings = append(findings, found...)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("checking %s: %w", root, err)
	}

	return findings, nil
}

// dirAction reports whether the walk should descend into a directory or skip it whole.
func dirAction(path, name string) error {
	if path == "." || name == "." {
		return nil
	}

	if skippedDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return fs.SkipDir
	}

	return nil
}

// inBodyFindings reports every comment that sits inside a function body.
func inBodyFindings(fset *token.FileSet, file *ast.File) []Finding {
	bodies := bodySpans(file)

	var findings []Finding

	for _, group := range file.Comments {
		for _, comment := range group.List {
			if isExempt(comment.Text) || !withinAny(comment.Pos(), bodies) {
				continue
			}

			findings = append(findings, newFinding(fset, comment.Pos(), msgInBody))
		}
	}

	return findings
}

// longDocFindings reports every doc comment running to more than one significant line.
func longDocFindings(fset *token.FileSet, file *ast.File) []Finding {
	var findings []Finding

	ast.Inspect(file, func(node ast.Node) bool {
		doc := governedDoc(node)
		if doc != nil && significantLines(doc) > 1 {
			findings = append(findings, newFinding(fset, doc.Pos(), msgLongDoc))
		}

		return true
	})

	return findings
}

// span is the brace-to-brace source range of one function body.
type span struct {
	lbrace token.Pos
	rbrace token.Pos
}

// bodySpans collects the range of every function body in the file, literals included.
func bodySpans(file *ast.File) []span {
	var spans []span

	ast.Inspect(file, func(node ast.Node) bool {
		if body := bodyOf(node); body != nil {
			spans = append(spans, span{lbrace: body.Lbrace, rbrace: body.Rbrace})
		}

		return true
	})

	return spans
}

// bodyOf returns the block a function node encloses, or nil for any other node.
func bodyOf(node ast.Node) *ast.BlockStmt {
	switch fn := node.(type) {
	case *ast.FuncDecl:
		return fn.Body
	case *ast.FuncLit:
		return fn.Body
	default:
		return nil
	}
}

// withinAny reports whether a position falls strictly inside any of the given spans.
func withinAny(pos token.Pos, spans []span) bool {
	for _, s := range spans {
		if pos > s.lbrace && pos < s.rbrace {
			return true
		}
	}

	return false
}

// governedDoc returns the doc comment of a declaration the one-line rule covers.
func governedDoc(node ast.Node) *ast.CommentGroup {
	switch decl := node.(type) {
	case *ast.FuncDecl:
		return decl.Doc
	case *ast.TypeSpec:
		return decl.Doc
	case *ast.Field:
		return decl.Doc
	case *ast.GenDecl:
		return typeDeclDoc(decl)
	default:
		return nil
	}
}

// typeDeclDoc returns a general declaration's doc comment only when it declares a type.
func typeDeclDoc(decl *ast.GenDecl) *ast.CommentGroup {
	if decl.Tok != token.TYPE {
		return nil
	}

	return decl.Doc
}

// significantLines counts the doc lines carrying prose rather than tooling directives.
func significantLines(doc *ast.CommentGroup) int {
	count := 0

	for _, comment := range doc.List {
		if strings.HasPrefix(comment.Text, "// Deprecated:") {
			break
		}

		count += proseLines(comment.Text)
	}

	return count
}

// proseLines counts the prose lines a single comment contributes to its group.
func proseLines(text string) int {
	if isExempt(text) {
		return 0
	}

	if !strings.HasPrefix(text, "//") {
		return strings.Count(text, "\n") + 1
	}

	if strings.TrimSpace(strings.TrimPrefix(text, "//")) == "" {
		return 0
	}

	return 1
}

// isExempt reports whether a comment instructs tooling rather than explaining code.
func isExempt(text string) bool {
	for _, prefix := range exemptPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}

	return false
}

// newFinding resolves a position to file, line, and column, and attaches the message.
func newFinding(fset *token.FileSet, pos token.Pos, message string) Finding {
	at := fset.Position(pos)

	return Finding{Path: at.Filename, Line: at.Line, Column: at.Column, Message: message}
}

// sortFindings orders findings by position so repeated runs report them identically.
func sortFindings(findings []Finding) {
	slices.SortFunc(findings, func(a, b Finding) int {
		return cmp.Or(
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Column, b.Column),
		)
	})
}
