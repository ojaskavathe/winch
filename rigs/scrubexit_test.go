package rigs

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestScrubExitClean: committing onto the window the sidebar is ALREADY
// docked in (Enter after scrubbing away and back, or clicking a split of
// that window) leaves the zoomed scrub. That shrinks the pane 480 -> 26,
// which tmux would reflow into a wall of wrapped canvas text — the reason
// this path used to respawn the whole TUI process. Respawning cost a blank
// strip for the ~6ms the new process needed, one presented frame in which
// the entire window came back except the sidebar: the flicker.
//
// The TUI now holds the alternate screen, so the grid is CLIPPED instead of
// reflowed and the already-painted list survives the unzoom. What this rig
// pins: the strip still shows the list immediately after the commit, the
// selection never jumps, and the process was not replaced.
func TestScrubExitClean(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(600)

	// Scrub away and back so the selection ends where it started; Enter then
	// commits onto the docked window itself. Which direction leaves the
	// origin row depends on where the dock sits — at an edge one no-ops.
	_, back := scrubAway(r, sp)
	r.Chk("scrubbing after moving off the origin", r.Side().Width == r.prof.cols)
	r.SendKeys(sp, back)
	sleep(700)

	mark := benchSize(r)
	pid := paneStartPID(r, sp)
	r.SendKeys(sp, "Enter")
	r.await(5000, "unzoomed back to the strip", func() bool { return r.Side().Width == sideW })

	// The list must be there the moment the strip is back — no blank, no
	// reflowed canvas text.
	r.Chk("strip shows the list right after the commit", r.WaitUntil(600, func() bool {
		return strings.Contains(r.Capture(r.Side().Pane), "sessions")
	}))
	r.Chk("took the clip path, not a respawn", r.LogHas("unzoom=clip"))
	r.Chk("the TUI process survived", r.Side().Pane == sp && paneStartPID(r, sp) == pid)

	sels := selectionsPainted(r, mark)
	r.Chk("selection never jumps on exit", len(sels) <= 1)
	if len(sels) > 1 {
		t.Logf("selections painted: %q", sels)
	}

	// Same exit via a billboard click: scrub away, click a split of the
	// origin window's billboard to commit back into it.
	sp = r.Side().Pane
	_, back = scrubAway(r, sp)
	r.SendKeys(sp, back)
	sleep(700)
	mark = benchSize(r)
	r.Click(sp, sideW+20, 6)
	r.await(5000, "unzoomed after click", func() bool { return r.Side().Width == sideW })
	r.Chk("strip shows the list after a click commit", r.WaitUntil(600, func() bool {
		return strings.Contains(r.Capture(r.Side().Pane), "sessions")
	}))
	sels = selectionsPainted(r, mark)
	r.Chk("click exit: selection never jumps", len(sels) <= 1)

	r.D("toggle", r.CL)
	sleep(800)
}

// TestCrossWindowCommit: committing onto a DIFFERENT window moves the
// sidebar there with a geometry-free swap-pane — the same pane, the same
// process, no second TUI. It used to hand off to a freshly spawned TUI
// because swapping the ZOOMED sidebar rewrapped its canvas-filled grid into
// the strip; the alternate screen makes that shrink a clip instead, and the
// zoomed layout already paints the list in the first listW columns, so the
// sidebar lands showing exactly the list.
//
// What this pins: the client ends up in the target window, the sidebar
// process survives the move, and no presented frame shows an empty strip.
func TestCrossWindowCommit(t *testing.T) {
	// A live-shaped client (480x96, kitty, synchronized output): the artifact
	// is a redraw race, so it only appears at the real screen size with real
	// content to repaint.
	r := NewLive(t)
	for _, w := range []string{r.W1, r.W2, r.W3, r.P1} {
		r.T("split-window", "-d", "-t", w,
			`sh -c 'for i in $(seq 400); do echo "row $i CONTENT abcdefghijklmnopqrstuvwxyz 0123456789"; done; sleep 1000'`)
	}
	sleep(1200)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sp := r.Side().Pane
	sleep(600)

	// scrub to the OTHER session and commit there
	startWin := r.Side().Win
	startPID := paneStartPID(r, sp)
	scrubAway(r, sp)
	mark := benchSize(r)
	r.StartRecord()
	r.SendKeys(sp, "Enter")
	r.await(5000, "landed in the other session", func() bool {
		s := r.Side()
		return s.Win != startWin && s.Width == sideW
	})
	sleep(900)
	chunks := r.StopRecordT()

	r.Chk("client followed the sidebar", r.ClientWin() == r.Side().Win)
	r.Chk("the sidebar moved rather than being respawned",
		r.Side().Pane == sp && paneStartPID(r, sp) == startPID)
	r.Chk("arrived sidebar shows the list", strings.Contains(r.Capture(r.Side().Pane), "sessions"))
	sels := selectionsPainted(r, mark)
	r.Chk("arriving TUI paints its selection once", len(sels) <= 1)
	if len(sels) > 1 {
		t.Logf("selections painted: %q", sels)
	}

	// The real assertion: no PRESENTED frame may show an empty strip. The
	// arriving TUI must have painted before phase 2 switches the client onto
	// it — switching on its mere existence put an unpainted sidebar in front
	// of the user for the first frames in the new window.
	blank := blankStripFrames(chunks, r.prof.rows, r.prof.cols, sideW)
	t.Logf("frames showing an empty strip: %d", blank)
	r.Chk("no frame shows an empty strip", blank == 0)

	r.D("toggle", r.CL)
	sleep(800)
}

// scrubAway moves the selection off the origin row, whichever direction
// actually leaves it (at a list edge one of them no-ops), and returns the
// key that moved and the key that comes back.
func scrubAway(r *Rig, sp string) (away, back string) {
	away, back = "j", "k"
	r.SendKeys(sp, away)
	sleep(600)
	if r.Side().Width != r.prof.cols { // no scrub: that direction was the edge
		away, back = "k", "j"
		r.SendKeys(sp, away)
		sleep(600)
	}
	return away, back
}

// paneStartPID identifies the process in a pane, so a rig can tell a
// surviving TUI from a respawned one.
func paneStartPID(r *Rig, pane string) string {
	out, _ := r.TQ("display-message", "-p", "-t", pane, "#{pane_pid}")
	return strings.TrimSpace(out)
}

func benchSize(r *Rig) int64 {
	fi, err := os.Stat(r.Sock + ".tui-bench.log")
	if err != nil {
		return 0
	}
	return fi.Size()
}

var selRowRe = regexp.MustCompile(`tui list row \d+: ".*" -> ">(.*)"`)

// selectionsPainted returns the sequence of rows that became selected in the
// TUI bench log after byte offset off, collapsing repeats.
func selectionsPainted(r *Rig, off int64) []string {
	b, err := os.ReadFile(r.Sock + ".tui-bench.log")
	if err != nil || int64(len(b)) <= off {
		return nil
	}
	var out []string
	for _, m := range selRowRe.FindAllStringSubmatch(string(b[off:]), -1) {
		s := strings.TrimSpace(m[1])
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}
