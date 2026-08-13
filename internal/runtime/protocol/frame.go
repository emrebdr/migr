package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"
)

// MaxLine is the longest line a reader accepts, counting the newline that terminates it.
const MaxLine = 16 << 20

// bom is the byte-order mark docs/protocol.md §2.2 forbids on either stream, at any point.
var bom = []byte{0xef, 0xbb, 0xbf}

// Reader reads one message per line, and is spent the moment it meets a line it cannot frame.
type Reader struct {
	buf  *bufio.Reader
	line int
	err  error
}

// NewReader reads protocol messages from a stream, one per line.
func NewReader(r io.Reader) *Reader {
	return &Reader{buf: bufio.NewReader(r)}
}

// Read returns the next message, or the failure that ended the session for good.
func (r *Reader) Read() (*Message, error) {
	if r.err != nil {
		return nil, r.err
	}

	m, err := r.read()
	if err != nil {
		r.err = err
	}

	return m, err
}

// Line reports how many lines have been read, which is what a failure names.
func (r *Reader) Line() int {
	return r.line
}

// read takes lines until one carries a message, since an empty line is not one.
func (r *Reader) read() (*Message, error) {
	for {
		line, err := r.readLine()
		if err != nil {
			return nil, err
		}

		if len(line) == 0 {
			continue
		}

		return r.message(line)
	}
}

// readLine accumulates one line under a bounded budget, refusing a newline that never comes.
func (r *Reader) readLine() ([]byte, error) {
	var line []byte

	for {
		chunk, err := r.buf.ReadSlice('\n')
		line = append(line, chunk...)

		if len(line) > MaxLine {
			return nil, r.tooLong()
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if err != nil {
			return nil, r.ended(line, err)
		}

		r.line++

		return trimEOL(line), nil
	}
}

// tooLong refuses a line at the budget rather than after a delimiter that may never arrive.
func (r *Reader) tooLong() error {
	return NewErrorf(CodeParse, "line %d is longer than the %d MiB one message may be",
		r.line+1, MaxLine>>20)
}

// ended tells a stream that finished from one that was cut off part way through a message.
func (r *Reader) ended(line []byte, err error) error {
	if !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading line %d: %w", r.line+1, err)
	}

	if len(bytes.TrimSpace(line)) == 0 {
		return io.EOF
	}

	r.line++

	return NewErrorf(CodeParse,
		"line %d ends without a newline, so it is a message that was cut off", r.line)
}

// message turns one framed line into a message, refusing the encodings §2.2 rules out.
func (r *Reader) message(line []byte) (*Message, error) {
	if bytes.HasPrefix(line, bom) {
		return nil, NewErrorf(CodeParse,
			"line %d opens with a byte-order mark, which no stream here ever carries", r.line)
	}

	if !utf8.Valid(line) {
		return nil, NewErrorf(CodeParse, "line %d is not valid UTF-8", r.line)
	}

	m, err := Decode(line)
	if err != nil {
		return nil, r.attribute(err)
	}

	return m, nil
}

// attribute names the line a failure was found on, keeping the code the decoder chose.
func (r *Reader) attribute(err error) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}

	return &Error{
		Code:    e.Code,
		Message: fmt.Sprintf("line %d: %s", r.line, e.Message),
		Data:    e.Data,
	}
}

// trimEOL drops the terminator, accepting the carriage return §2.2 never emits.
func trimEOL(line []byte) []byte {
	return bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
}

// Refuse builds the one null-id error §2.6 permits, or nil when there is nobody left to tell.
func Refuse(err error) *Message {
	var e *Error
	if err == nil || !errors.As(err, &e) {
		return nil
	}

	return &Message{JSONRPC: JSONRPC, ID: NullID(), Error: e}
}

// Writer writes one message per line, flushed, and is safe for concurrent use.
type Writer struct {
	mu  sync.Mutex
	buf *bufio.Writer
}

// NewWriter writes protocol messages to a stream, one per line.
func NewWriter(w io.Writer) *Writer {
	return &Writer{buf: bufio.NewWriter(w)}
}

// Write renders one message and flushes it, so nothing waits in a buffer for the next one.
func (w *Writer) Write(m *Message) error {
	line, err := render(m)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.buf.Write(line); err != nil {
		return fmt.Errorf("writing the message: %w", err)
	}

	if err := w.buf.WriteByte('\n'); err != nil {
		return fmt.Errorf("terminating the message: %w", err)
	}

	if err := w.buf.Flush(); err != nil {
		return fmt.Errorf("flushing the message: %w", err)
	}

	return nil
}

// render checks a message against everything a sender owes and returns the bytes of its line.
func render(m *Message) ([]byte, error) {
	if m == nil {
		return nil, errors.New("there is no message to write")
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}

	if m.Error != nil {
		if err := m.Error.Validate(); err != nil {
			return nil, err
		}
	}

	line, err := Encode(m)
	if err != nil {
		return nil, err
	}

	return line, checkLine(line)
}

// checkLine refuses bytes that would arrive as something other than the one message they are.
func checkLine(line []byte) error {
	if bytes.ContainsAny(line, "\n\r") {
		return errors.New("the encoded message carries a newline, which would arrive as two messages")
	}

	if len(line)+1 > MaxLine {
		return fmt.Errorf("the encoded message is %d bytes, past the %d MiB one line may be",
			len(line), MaxLine>>20)
	}

	return nil
}
