package main

import (
	"strings"
	"testing"
)

// The sweep recognises a leaked scrub override by scrubFmtMark alone. If the
// format is ever rewritten without it, every override a dying daemon leaves
// behind becomes permanent — and invisible, since the bar still renders.
func TestScrubFmtMarked(t *testing.T) {
	got := scrubStatusFormat("$3", "@7")
	if !strings.Contains(got, scrubFmtMark) {
		t.Fatalf("scrubStatusFormat no longer carries scrubFmtMark %q:\n%s", scrubFmtMark, got)
	}
	// The mark must be ours, not something a theme would write: it has to
	// carry the filtered session loop, not just a session_id comparison.
	if !strings.Contains(scrubFmtMark, "#{S:") {
		t.Errorf("mark too generic to be safe to sweep on: %q", scrubFmtMark)
	}
}
