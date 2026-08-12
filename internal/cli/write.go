package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// newFileMode is what migr writes its files as, matching the mode the golden-file harness uses.
const newFileMode = 0o600

// newDirMode is what migr creates a directory as.
const newDirMode = 0o750

// writeJSON renders a result as the indented document --json promises on stdout.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encoding result: %w", err)
	}

	return nil
}

// writeResult renders a result as JSON or as the lines it renders itself to.
func writeResult(w io.Writer, opts *options, result fmt.Stringer) error {
	if opts.jsonOut {
		return writeJSON(w, result)
	}

	if _, err := io.WriteString(w, result.String()); err != nil {
		return fmt.Errorf("writing result: %w", err)
	}

	return nil
}

// writeNew creates a file that must not already exist, so nothing migr writes can overwrite anything.
func writeNew(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, newFileMode)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}

	if _, err := f.WriteString(content); err != nil {
		return errors.Join(fmt.Errorf("writing %s: %w", path, err), f.Close())
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}

	return nil
}
