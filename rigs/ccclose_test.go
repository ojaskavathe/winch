package rigs

import (
	"bytes"
	"os"
	"testing"
)

// The remaining user-visible artifact: closing the sidebar and landing in a
// Claude Code pane. Probe the real close flavors against a real claude
// pane and count presented frames (storm ~760, clean <20).
func TestCCCloseProbe(t *testing.T) {
	cc := os.Getenv("CCBIN")
	if cc == "" {
		t.Skip("set CCBIN=/path/to/claude")
	}
	r := NewLive(t)
	r.T("set-option", "-g", "focus-events", "on")
	r.T("respawn-pane", "-k", "-t", "play:0.0", cc)
	r.T("split-window", "-h", "-t", "play:0")
	sleep(6000)
	pane := r.T("display-message", "-p", "-t", "play:0.0", "#{pane_id}")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	r.T("select-pane", "-t", pane)
	sleep(800)
	frames := func(rec []byte) int { return bytes.Count(rec, []byte("\x1b[?2026h")) }

	// (a) open, settle past the handoff, close from the sidebar.
	r.D("toggle", r.CL)
	sleep(1500)
	r.StartRecord()
	r.D("toggle", r.CL)
	sleep(1000)
	t.Logf("close from sidebar (settled): %d frames", frames(r.StopRecord()))
	sleep(800)

	// (b) rapid double M-s from the CC pane: focus sidebar, then close.
	r.T("select-pane", "-t", pane)
	sleep(400)
	r.D("toggle", r.CL)
	sleep(1500)
	r.T("select-pane", "-t", pane) // keyboard back in cc, sidebar stays
	sleep(600)
	r.StartRecord()
	r.D("toggle", r.CL) // focuses sidebar
	sleep(60)
	r.D("toggle", r.CL) // closes
	sleep(1000)
	t.Logf("rapid M-s M-s from cc: %d frames", frames(r.StopRecord()))
	sleep(800)

	// (c) scrub, then M-s dismiss landing back on the cc window.
	r.T("select-pane", "-t", pane)
	sleep(400)
	r.D("toggle", r.CL)
	sleep(1500)
	sp := r.Side().Pane
	r.SendKeys(sp, "l") // zoom into scrub (to ptwo)
	sleep(700)
	r.SendKeys(sp, "h") // back onto the docked window's row
	sleep(500)
	r.StartRecord()
	r.D("toggle", r.CL) // dismiss-from-scrub
	sleep(1200)
	t.Logf("M-s dismiss from scrub: %d frames", frames(r.StopRecord()))
}
