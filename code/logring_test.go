package main

import (
	"strings"
	"testing"
)

func TestLogRingSanitizesAndBoundsOutput(t *testing.T) {
	r := newLogRing()
	r.Append("\x1b[31mfailed\x1b[0m\tto connect\x00")
	if got := r.Lines(); len(got) != 1 || got[0] != "failed to connect" {
		t.Fatalf("sanitized log = %#v", got)
	}

	for i := 0; i < ringMaxLines+10; i++ {
		r.Append(strings.Repeat("x", ringMaxLineLen+20))
	}
	lines := r.Lines()
	if len(lines) > ringMaxLines {
		t.Fatalf("ring retained %d lines, max is %d", len(lines), ringMaxLines)
	}
	for _, line := range lines {
		if len(line) > ringMaxLineLen+len("…") {
			t.Fatalf("line was not capped: %d bytes", len(line))
		}
	}
}
