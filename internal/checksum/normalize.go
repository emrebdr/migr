package checksum

import "bytes"

// The line endings a checkout can rewrite without producing a diff anyone can see.
var (
	crlf = []byte("\r\n")
	cr   = []byte("\r")
	lf   = []byte("\n")
)

// normalize rewrites the line endings version control changes, and nothing an author typed.
func normalize(source []byte) []byte {
	return bytes.ReplaceAll(bytes.ReplaceAll(source, crlf, lf), cr, lf)
}
