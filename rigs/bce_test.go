package rigs

import (
	"regexp"
	"strings"
	"testing"
)

// TestBCEBillboard: TUIs paint full-width bars as bg + erase-to-EOL (BCE);
// capture-pane drops the fill and leaves the bg OPEN at line end. The
// billboard painter must pad to the pane edge in that live state so the bar
// spans the cell like the real pane — not stop dead at the last glyph.
func TestBCEBillboard(t *testing.T) {
	r := New(t)

	shell := ""
	for _, ln := range strings.Split(r.T("list-panes", "-t", r.W1, "-F", "#{pane_id} #{pane_start_command}"), "\n") {
		f := strings.SplitN(ln, " ", 2)
		if len(f) == 2 && !strings.Contains(f[1], "MARKW1") {
			shell = f[0]
		}
	}
	// B=... keeps the markers out of the echoed command line, so only the
	// printed output contains the contiguous strings. Second line: an OSC 8
	// hyperlink (grok's TUI emits these) — its payload is zero-width and
	// must not skew padding or leak into the billboard.
	// Third block: a bg opened on one row and NEVER reset — capture emits
	// the following row with no sequence at all (state carries across
	// lines); the frame must self-contain it or the row paints black.
	// Fourth block: the same bar followed by a genuinely BLANK row. Claude
	// Code paints user messages this way, and carrying the open bg one row
	// too far filled the row underneath — the two cases are only separable
	// in a -N capture.
	r.SendKeys(shell,
		`clear; B=BCE; printf '\033[44m'"$B"'BAR\033[K\033[0m\n\033]8;;http://osc-leak.test\033\\'"$B"'LINK\033]8;;\033\\ \033[45m'"$B"'BAR2\033[K\033[0m\n\033[48;2;30;30;99m\033[K\n'"$B"'CARRY\033[0m\n\033[48;2;7;77;177m'"$B"'GAP\033[K\033[0m\n\n'"$B"'AFTER\033[0m\n'`,
		"Enter")
	sleep(500)

	r.D("toggle", r.CL) // dock on beta
	sleep(800)
	sp := r.Side().Pane
	r.SendKeys(sp, "h") // billboard w1
	sleep(900)

	// poll: under full-suite CPU load the billboard paint can land seconds
	// late — the content is what matters, not the latency
	bar := regexp.MustCompile(`\x1b\[[0-9;]*44m[^\x1b]*BCEBAR {10,}`)
	out := ""
	r.WaitUntil(5000, func() bool {
		out, _ = r.TQ("capture-pane", "-e", "-p", "-t", sp)
		return bar.MatchString(out)
	})
	r.Chk("billboard shows the bar", strings.Contains(out, "BCEBAR"))
	// the bg-open padding: the real bar (ESC[44m, not echoed text) runs on
	// in spaces still inside the 44 background, not an immediate reset
	r.Chk("bar extends past the text", bar.MatchString(out))
	r.Chk("hyperlink text kept", strings.Contains(out, "BCELINK"))
	r.Chk("OSC payload stripped", !strings.Contains(out, "osc-leak.test"))
	r.Chk("bar after link intact", regexp.MustCompile(`45m[^\x1b]*BCEBAR2 {5,}`).MatchString(out))
	r.Chk("carried bg reaches its row", regexp.MustCompile(`48;2;30;30;99m[^\x1b]*BCECARRY`).MatchString(out))

	// The bar's bg must occupy exactly ONE row: its own. Before -N, the blank
	// row below inherited the open bg and got padded to the pane edge, so the
	// colour appeared on two rows.
	gapBg := regexp.MustCompile(`48[;:]2[;:]7[;:]77[;:]177`)
	r.WaitUntil(5000, func() bool {
		out, _ = r.TQ("capture-pane", "-e", "-p", "-t", sp)
		return strings.Contains(out, "BCEGAP")
	})
	r.Chk("gap bar reached the billboard", strings.Contains(out, "BCEGAP"))
	rows := 0
	for _, ln := range strings.Split(out, "\n") {
		if gapBg.MatchString(ln) {
			rows++
			t.Logf("gap-bg row: %q", ln)
		}
	}
	r.Chk("blank row under a BCE bar does not inherit its bg", rows == 1)
	if rows != 1 {
		t.Logf("  bar bg painted on %d rows, want 1", rows)
	}

	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "BCEBAR") {
			t.Logf("bar line: %q", ln)
		}
	}
}
