package main

import (
	"strings"
	"testing"
)

func TestSplitAtPlain(t *testing.T) {
	rows, widths := splitAt("abcdefghij", 4)
	if want := []string{"abcd", "efgh", "ij"}; !eqStrs(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
	if widths[0] != 4 || widths[2] != 2 {
		t.Errorf("widths = %v", widths)
	}
}

func TestSplitAtEmpty(t *testing.T) {
	rows, widths := splitAt("", 10)
	if len(rows) != 1 || rows[0] != "" || widths[0] != 0 {
		t.Errorf("empty line = %q %v, want one empty row", rows, widths)
	}
}

func TestSplitAtEscapesZeroWidth(t *testing.T) {
	// The SGR must ride with the rune it precedes, never split a row early.
	rows, _ := splitAt("ab\x1b[31mcd\x1b[0mef", 4)
	if want := []string{"ab\x1b[31mcd\x1b[0m", "ef"}; !eqStrs(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
}

func TestSplitAtWideStraddle(t *testing.T) {
	// Width 3: "ab" fits, the wide rune would straddle the boundary and must
	// start the next row; with its padding cell counted (tmux's accounting)
	// the 世 row is full at units 3, so "c" starts the row after.
	rows, widths := splitAt("ab世cd", 3)
	if want := []string{"ab", "世", "cd"}; !eqStrs(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
	if widths[0] != 2 || widths[1] != 2 || widths[2] != 2 {
		t.Errorf("widths = %v", widths)
	}
}

// The joined inputs below model what capture -J -E <h-1> really returns: the
// FULL pane height, trailing blank rows included as empty lines (probed on
// 3.7b) — the bottom-anchored cursor walk counts on that coverage.

func TestEmulatePaneNarrow(t *testing.T) {
	// 3 logical lines + a trailing blank fill a 10x4 pane; narrowed to 5 the
	// long line doubles, "first" scrolls into history, the blank stays put.
	joined := []string{"first", "0123456789", "last", ""}
	ep := emulatePane(joined, 0, 5, 4, 2, 4, true)
	if want := []string{"01234", "56789", "last", ""}; !eqStrs(ep.rows, want) {
		t.Errorf("rows = %q, want %q", ep.rows, want)
	}
	// Cursor was at col 4 of "last" (visible row 2); "last" is now row 2.
	if !ep.cursor || ep.cy != 2 || ep.cx != 4 {
		t.Errorf("cursor = %v (%d,%d), want on row 2 col 4", ep.cursor, ep.cx, ep.cy)
	}
}

func TestEmulatePaneCursorWraps(t *testing.T) {
	// Cursor at col 7 of a 10-wide line; at width 5 that cell is row 1 col 2.
	joined := []string{"0123456789", ""}
	ep := emulatePane(joined, 0, 5, 4, 0, 7, true)
	if !ep.cursor || ep.cy != 1 || ep.cx != 2 {
		t.Errorf("cursor = %v (%d,%d), want (2,1)", ep.cursor, ep.cx, ep.cy)
	}
	if want := []string{"01234", "56789", ""}; !eqStrs(ep.rows, want) {
		t.Errorf("rows = %q, want %q", ep.rows, want)
	}
}

func TestEmulatePaneSGRCarry(t *testing.T) {
	// The red set on the first line scrolls out of the 2-row window; the
	// carry must re-seed it on the first kept row.
	joined := []string{"\x1b[31mred starts here", "aaaaaaaaaa", "bbbbbbbbbb"}
	ep := emulatePane(joined, 0, 10, 2, 0, 0, false)
	if want := []string{"aaaaaaaaaa", "bbbbbbbbbb"}; len(ep.rows) != 2 ||
		!strings.HasSuffix(ep.rows[0], want[0]) || ep.rows[1] != want[1] {
		t.Fatalf("rows = %q", ep.rows)
	}
	if !strings.Contains(ep.rows[0], "31m") {
		t.Errorf("first row %q lost the red carry", ep.rows[0])
	}
}

func TestEmulatePaneBlankRowsSurvive(t *testing.T) {
	joined := []string{"top", "", "bottom"}
	ep := emulatePane(joined, 0, 10, 5, 0, 0, false)
	if want := []string{"top", "", "bottom"}; !eqStrs(ep.rows, want) {
		t.Errorf("rows = %q, want %q", ep.rows, want)
	}
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSplitAtPaddingAccounting(t *testing.T) {
	// The measured 3.7b behavior: 10a+世+36b at width 30 splits after 17 b's
	// (display column 29) because the wide rune's padding cell counts too.
	line := strings.Repeat("a", 10) + "世" + strings.Repeat("b", 36)
	rows, widths := splitAt(line, 30)
	if len(rows) != 2 || widths[0] != 29 || widths[1] != 19 {
		t.Fatalf("rows=%d widths=%v, want 2 rows [29 19]", len(rows), widths)
	}
	if !strings.HasSuffix(rows[0], "世"+strings.Repeat("b", 17)) {
		t.Errorf("row0 = %q, want split after 17 b's", rows[0])
	}
}
