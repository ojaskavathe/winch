package rigs

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// Probe: a primary-screen app with focus reporting (fake Claude Code) split
// beside a plain pane. Compare what the client is shown for: focus-only
// change, plain one-shot resize, winch open with the fake focused, and
// winch open with the OTHER pane focused. The user's report: CC flickers
// only when it is the active pane at open.
func TestCCFocusProbe(t *testing.T) {
	r := NewLive(t)
	script := os.Getenv("FAKECC")
	if script == "" {
		t.Skip("set FAKECC=/path/to/fakecc.pl")
	}
	r.T("set-option", "-g", "focus-events", "on")
	r.T("respawn-pane", "-k", "-t", "play:0.0", script)
	r.T("split-window", "-h", "-t", "play:0")
	sleep(1200)
	cc := r.T("display-message", "-p", "-t", "play:0.0", "#{pane_id}")
	other := r.T("display-message", "-p", "-t", "play:0.1", "#{pane_id}")
	if !bytes.Contains([]byte(r.Capture(cc)), []byte("CCBOX")) {
		t.Fatalf("fake cc not painting: %q", r.Capture(cc))
	}
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	r.T("select-pane", "-t", cc)
	sleep(500)

	analyze := func(name string, rec []byte) {
		frames := bytes.Split(rec, []byte("\x1b[?2026h"))
		ccPaints, clears := 0, 0
		for _, f := range frames {
			if bytes.Contains(f, []byte("CCBOX")) {
				ccPaints++
			}
			if bytes.Contains(f, []byte("\x1b[2J")) {
				clears++
			}
		}
		t.Logf("%-28s: %2d frames, %d repaint fake-cc, %d with clear-screen", name, len(frames)-1, ccPaints, clears)
	}

	rec := func(name string, act func(), settle int) {
		r.StartRecord()
		act()
		sleep(settle)
		chunks := r.StopRecordT()
		var all []byte
		var times []string
		for _, c := range chunks {
			if bytes.Contains(c.Data, []byte("CCBOX")) {
				times = append(times, fmt.Sprintf("+%dms", c.At.Milliseconds()))
			}
			all = append(all, c.Data...)
		}
		analyze(name, all)
		t.Logf("%-28s: cc repaints at %v", name, times)
	}

	rec("focus-only (select-pane away)", func() { r.T("select-pane", "-t", other) }, 500)
	rec("focus back", func() { r.T("select-pane", "-t", cc) }, 500)
	w, _ := strconv.Atoi(r.T("display-message", "-p", "-t", cc, "#{pane_width}"))
	rec("plain 45-col resize", func() { r.T("resize-pane", "-t", cc, "-x", strconv.Itoa(w-45)) }, 700)
	rec("plain resize back", func() { r.T("resize-pane", "-t", cc, "-x", strconv.Itoa(w)) }, 700)
	rec("winch open, fake-cc focused", func() { r.D("toggle", r.CL) }, 900)
	rec("winch close", func() {
		r.D("toggle", r.CL)
		sleep(200)
		if r.Side().Pane != "" {
			r.D("toggle", r.CL)
		}
	}, 700)
	r.T("select-pane", "-t", other)
	sleep(300)
	rec("winch open, other focused", func() { r.D("toggle", r.CL) }, 900)
}
