package main

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Per-forward capture of the tunwg child's combined stdout+stderr. The ring
// keeps roughly the last ringMaxLines lines (bounded to ringMaxBytes total)
// so a crash can be explained after the fact without persisting unbounded
// process output.
const (
	ringMaxLines   = 120
	ringMaxBytes   = 16 * 1024
	ringMaxLineLen = 512
	logTailLines   = 20 // lines kept as LastLog when the process exits
)

// ansiRe matches ANSI escape sequences: CSI sequences (colors, cursor
// movement), OSC sequences (terminated by BEL or ST) and two-byte escapes.
var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[@-Z\\-_])`)

// sanitizeLogLine strips ANSI escapes and control characters (tabs are kept
// as spaces) and caps the line length at ringMaxLineLen bytes.
func sanitizeLogLine(line string) string {
	line = ansiRe.ReplaceAllString(line, "")
	line = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, line)
	if len(line) > ringMaxLineLen {
		cut := ringMaxLineLen
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "…"
	}
	return line
}

// logRing is a bounded, thread-safe buffer of sanitized log lines.
type logRing struct {
	mu    sync.Mutex
	lines []string
	bytes int
}

func newLogRing() *logRing {
	return &logRing{}
}

// Append sanitizes one raw output line and adds it, evicting the oldest
// lines to stay within the line and byte budgets.
func (r *logRing) Append(raw string) {
	line := sanitizeLogLine(raw)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	r.bytes += len(line)
	for len(r.lines) > ringMaxLines || (r.bytes > ringMaxBytes && len(r.lines) > 1) {
		r.bytes -= len(r.lines[0])
		r.lines = r.lines[1:]
	}
}

// Lines returns a copy of the buffered lines, oldest first.
func (r *logRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// Tail returns a copy of the most recent n lines.
func (r *logRing) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := len(r.lines) - n
	if start < 0 {
		start = 0
	}
	out := make([]string, len(r.lines)-start)
	copy(out, r.lines[start:])
	return out
}

// Reset clears the buffer (called when a fresh child process starts, so the
// ring always describes the current or most recent run).
func (r *logRing) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = nil
	r.bytes = 0
}
