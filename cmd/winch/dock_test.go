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

// tmux splits a format conditional at the first comma not inside #{}, and it
// does NOT count #[] — so one #[bg=x,fg=y] anywhere in a branch truncates it,
// and the rest of the pad silently disappears. Probe-verified, and the reason
// every style in the pad is written as separate directives.
//
// Cheap to state and impossible to notice by reading: the format still renders,
// just wrong, and only in the branch that was taken.
func TestPadHasNoCommaInsideAStyle(t *testing.T) {
	saveTheme, saveSeam, saveLines := uiTheme, uiSeamStyle, uiBorderLines
	defer func() { uiTheme, uiSeamStyle, uiBorderLines = saveTheme, saveSeam, saveLines }()
	// A style with a comma in it is exactly the case that has to survive:
	// catppuccin's border style is a whole format chain, and a user's can be
	// anything at all.
	uiTheme, uiSeamStyle, uiBorderLines = "catppuccin", "fg=#b4befe,bold", "single"

	for _, row := range []int{0, 1} {
		got := padPrefix(26, row)
		rest := got
		for {
			i := strings.Index(rest, "#[")
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], "]")
			if j < 0 {
				t.Fatalf("row %d: unterminated style directive in %q", row, got)
			}
			if inner := rest[i+2 : i+j]; strings.Contains(inner, ",") {
				t.Errorf("row %d: #[%s] holds a comma, which truncates the branch it is in:\n%s",
					row, inner, got)
			}
			rest = rest[i+j:]
		}
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
		{"the glyph-era pad", pad(26) + "#{?#{==:#{status},on},│, }#[default]", true},
		{"unset", "", false},
		{"a themed session name", "#[fg=blue] #S #[default]", false},
		{"spaces, but too few to be a sidebar", pad(4), false},
		{"padding around real content", "   #S   ", false},
		{"a visible bar that merely starts wide", "#[bg=black,fg=white]" + strings.Repeat(" ", 30) + "#S", false},
	} {
		if got := legacyPad(c.in); got != c.want {
			t.Errorf("%s: legacyPad(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
