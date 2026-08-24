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

// The pad written by the current build must carry padWin, which is the only
// thing the format sweep recognises. Lose the tie and a daemon that dies while
// docked leaves the bar shifted forever.
func TestPadMarked(t *testing.T) {
	if got := padPrefix(26, 0); !strings.Contains(got, padWin) {
		t.Fatalf("padPrefix no longer carries padWin %q:\n%s", padWin, got)
	}
}

// legacyPad decides whether to delete a session's status-left, so it has to
// recognise the old pad and nothing a person would write.
func TestLegacyPadDetect(t *testing.T) {
	pad := func(w int) string {
		return "#[bg=#181825,fg=#181825]" + strings.Repeat(" ", w) + "#[default]"
	}
	for _, c := range []struct {
		name string
		in   string
		want bool
	}{
		{"a real 27-column pad", pad(27), true},
		{"a real 41-column pad", pad(41), true},
		{"unset", "", false},
		{"a themed session name", "#[fg=blue] #S #[default]", false},
		{"spaces, but too few to be a sidebar", pad(4), false},
		{"padding around real content", "   #S   ", false},
	} {
		if got := legacyPad(c.in); got != c.want {
			t.Errorf("%s: legacyPad(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
