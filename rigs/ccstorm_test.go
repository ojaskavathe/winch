package rigs

import (
	"bytes"
	"os"
	"strconv"
	"testing"
)

// Narrow the CC render-storm trigger: which combination of resize and focus
// events sets it off, and how much separation defuses it.
func TestCCStormProbe(t *testing.T) {
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
	other := r.T("display-message", "-p", "-t", "play:0.1", "#{pane_id}")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(800)

	frames := func(rec []byte) int { return bytes.Count(rec, []byte("\x1b[?2026h")) }
	w, _ := strconv.Atoi(r.T("display-message", "-p", "-t", pane, "#{pane_width}"))
	narrow, wide := strconv.Itoa(w-45), strconv.Itoa(w)

	run := func(name string, act func()) {
		r.T("select-pane", "-t", pane) // start focused, like the user
		r.T("resize-pane", "-t", pane, "-x", wide)
		sleep(800)
		r.StartRecord()
		act()
		sleep(900)
		t.Logf("%-40s: %d frames", name, frames(r.StopRecord()))
	}

	run("resize;focus-out same batch (old open)", func() {
		r.T("resize-pane", "-t", pane, "-x", narrow, ";", "select-pane", "-t", other)
	})
	run("focus-out;resize same batch", func() {
		r.T("select-pane", "-t", other, ";", "resize-pane", "-t", pane, "-x", narrow)
	})
	run("resize, focus-out +50ms (deferred)", func() {
		r.T("resize-pane", "-t", pane, "-x", narrow)
		sleep(50)
		r.T("select-pane", "-t", other)
	})
	run("resize, focus-out +150ms", func() {
		r.T("resize-pane", "-t", pane, "-x", narrow)
		sleep(150)
		r.T("select-pane", "-t", other)
	})
	run("resize, focus-out +400ms", func() {
		r.T("resize-pane", "-t", pane, "-x", narrow)
		sleep(400)
		r.T("select-pane", "-t", other)
	})
	// close shape: pane is narrow + unfocused, then widen+focus-in together
	r.T("select-pane", "-t", other)
	r.T("resize-pane", "-t", pane, "-x", narrow)
	sleep(800)
	r.StartRecord()
	r.T("resize-pane", "-t", pane, "-x", wide, ";", "select-pane", "-t", pane)
	sleep(900)
	t.Logf("%-40s: %d frames", "widen;focus-in same batch (close)", frames(r.StopRecord()))
}

// TestCCWinchOpen drives the real toggle against a focused real-CC pane and
// asserts the render storm stays gone: with the dockFocusDelay handoff the
// open presents tens of frames at most, not ~760.
func TestCCWinchOpen(t *testing.T) {
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

	r.StartRecord()
	r.D("toggle", r.CL) // open with the CC pane focused
	sleep(1000)
	open := bytes.Count(r.StopRecord(), []byte("\x1b[?2026h"))

	r.StartRecord()
	r.D("toggle", r.CL) // sidebar focused by now: this closes
	sleep(1000)
	closed := bytes.Count(r.StopRecord(), []byte("\x1b[?2026h"))

	t.Logf("open=%d frames, close=%d frames", open, closed)
	if open > 60 {
		t.Errorf("open render storm: %d frames", open)
	}
	if closed > 60 {
		t.Errorf("close render storm: %d frames", closed)
	}
}
