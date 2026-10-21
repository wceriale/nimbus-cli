// Package output implements the CLI's output contract: exactly one JSON
// envelope on stdout when the caller is a program, readable text otherwise.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Error is what every failed command reports. Code is the stable contract
// agents branch on, and Hint names the next step.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
	LogTail []string       `json:"log_tail,omitempty"`

	usage bool
}

func (e *Error) Error() string { return e.Message }

// ExitCode is 2 for bad usage and 1 for every other failure.
func (e *Error) ExitCode() int {
	if e.usage {
		return 2
	}
	return 1
}

// Errorf builds an Error with a formatted message.
func Errorf(code, hint, format string, args ...any) *Error {
	return &Error{Code: code, Hint: hint, Message: fmt.Sprintf(format, args...)}
}

// Usage reports a bad invocation: an unknown command, a bad flag, or the
// wrong number of arguments.
func Usage(message, hint string) *Error {
	return &Error{Code: "usage", Message: message, Hint: hint, usage: true}
}

// Texter is implemented by results that have a human-readable form.
type Texter interface{ Text() string }

// Printer writes results in the mode chosen for this run.
type Printer struct {
	Stdout io.Writer
	Stderr io.Writer
	JSON   bool
}

type envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// Success prints a command's result.
func (p *Printer) Success(data any) {
	if p.JSON {
		p.writeJSON(envelope{OK: true, Data: data})
		return
	}
	if t, ok := data.(Texter); ok {
		fmt.Fprintln(p.Stdout, t.Text())
		return
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	fmt.Fprintln(p.Stdout, string(b))
}

// Failure prints an error: as the JSON envelope on stdout, or as text on stderr.
func (p *Printer) Failure(e *Error) {
	if p.JSON {
		p.writeJSON(envelope{OK: false, Error: e})
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Error: %s\n", e.Message)
	if len(e.LogTail) > 0 {
		b.WriteString("\n")
		for _, line := range e.LogTail {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		b.WriteString("\n")
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "Hint: %s\n", e.Hint)
	}
	io.WriteString(p.Stderr, b.String())
}

// Progress reports a step of a long command on stderr, so stdout stays a
// single result.
func (p *Printer) Progress(format string, args ...any) {
	fmt.Fprintf(p.Stderr, format+"\n", args...)
}

func (p *Printer) writeJSON(v any) {
	enc := json.NewEncoder(p.Stdout)
	// Hints contain things like `nimbus share <email>`; keep them readable.
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
