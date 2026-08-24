package rigs

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestPaintCost pins the canvas painter's cost model at a live-shaped client.
// Two things used to make every scrub step expensive:
//
//   - the diff baseline was keyed on window IDENTITY, so moving the selection
//     (i.e. changing window) always took the full-repaint branch;
//   - a full repaint blanked the WHOLE preview region first — 43.8KB of
//     spaces at 480x96, 57% of a measured full paint — all of it immediately
//     overwritten, because panes tile the region.
//
// A scrub step between same-shaped windows must now be a line diff, and any
// full repaint must blank only what the incoming layout does not cover.
func TestPaintCost(t *testing.T) {
	r := NewLive(t)
	// Both scrub targets are single-pane, so geometry matches and the step
	// is a pure content change — the common case when paging. Fill each to
	// full height with DIFFERENT text: diffing across windows must not leave
	// a single line of the other one behind. Note a session row billboards
	// that session's ACTIVE window, not its first.
	fillWindow(r, r.T("display-message", "-p", "-t", "play:", "#{window_id}"), "PLAYONE")
	fillWindow(r, r.T("display-message", "-p", "-t", "work:", "#{window_id}"), "WORKBETA")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(700)

	// World churn that changes no ROW: the list is sessions-only, so windows
	// coming and going rebuild the rows to the same bytes. Each one still
	// reaches the TUI as a diff and triggers a paint. (Panes would not do:
	// tmux emits no notification for pane changes outside the attached
	// window, so the world would never diff at all.)
	idleMark := benchSize(r)
	for i := 0; i < 3; i++ {
		w := r.T("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "play:")
		sleep(300)
		r.TQ("kill-window", "-t", w)
		sleep(300)
	}
	written, skipped := listPaintCounts(r, idleMark)
	t.Logf("idle churn: %d list paints written, %d skipped as identical", written, skipped)
	r.Chk("row-less world churn does not rewrite the strip", skipped > 0 && written == 0)

	scrubAway(r, sp) // enter the scrub (zooms)
	sleep(700)

	mark := benchSize(r)
	// Page between the two sessions repeatedly. Whichever row the list
	// happens to order first, the invariant is the same: the canvas shows
	// ONE window's content, never a mix — a diff that skipped a line it
	// should have rewritten would leave the other marker on screen.
	_, back := scrubAway(r, sp)
	away := map[string]string{"j": "k", "k": "j"}[back]
	seen := map[string]bool{}
	mixed := ""
	for i := 0; i < 4; i++ {
		for _, k := range []string{back, away} {
			r.SendKeys(sp, k)
			sleep(500)
			cap := r.Capture(sp)
			a, b := strings.Contains(cap, "PLAYONE"), strings.Contains(cap, "WORKBETA")
			if a && b && mixed == "" {
				mixed = "canvas showed PLAYONE and WORKBETA at once"
			}
			seen["PLAYONE"] = seen["PLAYONE"] || a
			seen["WORKBETA"] = seen["WORKBETA"] || b
		}
	}
	r.Chk("both windows were actually billboarded", seen["PLAYONE"] && seen["WORKBETA"])
	r.Chk("no stale content survives a cross-window diff", mixed == "")
	if mixed != "" {
		t.Logf("  %s", mixed)
	}

	full, diff := paintFrameCosts(r, mark)
	t.Logf("canvas paints: %d full %v, %d diffed %v", len(full), full, len(diff), diff)

	written, skipped = listPaintCounts(r, mark)
	t.Logf("scrub steps: %d list paints written, %d skipped", written, skipped)

	// Same-shaped windows: the steps must diff, not full-repaint.
	r.Chk("scrub steps diff instead of full-repainting", len(diff) > len(full))

	// Whatever full paints remain must not carry a whole-region blank fill.
	// The old prefill alone was height*avail spaces; anything near that means
	// the region is still being blanked wholesale.
	prefill := (r.prof.rows - 1) * (r.prof.cols - sideW - 1)
	for _, b := range full {
		if b >= prefill {
			r.Chk("full repaint no longer blanks the whole region", false)
			t.Logf("  full paint of %d bytes >= whole-region prefill of %d", b, prefill)
			break
		}
	}

	r.SendKeys(sp, "q")
	sleep(600)
	r.D("toggle", r.CL)
	sleep(800)
}

// fillWindow paints a window's active pane full of a distinctive marker and
// waits until it is really on screen. Keys sent before the shell finished
// starting (direnv, rc files) are silently swallowed, so this retries rather
// than assuming the first send landed.
func fillWindow(r *Rig, win, marker string) {
	r.t.Helper()
	pane := r.T("display-message", "-p", "-t", win, "#{pane_id}")
	for i := 0; i < 4; i++ {
		r.T("send-keys", "-t", pane,
			"clear; for i in $(seq 200); do echo "+marker+" $i; done", "Enter")
		if r.WaitUntil(200, func() bool { return strings.Contains(r.Capture(pane), marker) }) {
			return
		}
	}
	r.t.Fatalf("could not fill %s with %s", win, marker)
}

// listPaintCounts returns how many strip paints were emitted versus skipped
// as byte-identical to what is already on screen.
func listPaintCounts(r *Rig, off int64) (written, skipped int) {
	b, err := os.ReadFile(r.Sock + ".tui-bench.log")
	if err != nil || int64(len(b)) <= off {
		return 0, 0
	}
	for _, ln := range strings.Split(string(b[off:]), "\n") {
		switch {
		case strings.Contains(ln, "paint_list skipped"):
			skipped++
		case strings.Contains(ln, "paint_list dur_us"):
			written++
		}
	}
	return written, skipped
}

var paintFrameRe = regexp.MustCompile(`paint_frame dur_us=\d+ bytes=(\d+) panes=\d+ diff=(true|false)`)

// paintFrameCosts returns the byte sizes of full and diffed canvas paints
// recorded after byte offset off.
func paintFrameCosts(r *Rig, off int64) (full, diff []int) {
	b, err := os.ReadFile(r.Sock + ".tui-bench.log")
	if err != nil || int64(len(b)) <= off {
		return nil, nil
	}
	for _, m := range paintFrameRe.FindAllStringSubmatch(string(b[off:]), -1) {
		n, _ := strconv.Atoi(m[1])
		if strings.HasSuffix(m[2], "true") {
			diff = append(diff, n)
		} else {
			full = append(full, n)
		}
	}
	return full, diff
}
