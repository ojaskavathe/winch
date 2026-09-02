package rigs

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Live-shaped close: a real claude pane with a large unsent paste (slow
// re-render) beside a heavy-history pane (close pays a reflow stall) in a
// five-pane 480-column window. Records the close and reports the presented
// timeline: an erase-heavy chunk followed by a visible gap before content
// is the user's blank flash.
func TestCCHeavyCloseProbe(t *testing.T) {
	cc := os.Getenv("CCBIN")
	if cc == "" {
		t.Skip("set CCBIN=/path/to/claude")
	}
	r := NewLive(t)
	r.T("set-option", "-g", "focus-events", "on")
	r.T("set-option", "-g", "history-limit", "1000000")
	r.T("respawn-pane", "-k", "-t", "play:0.0", cc)
	for i := 0; i < 4; i++ {
		r.T("split-window", "-h", "-t", "play:0")
	}
	r.T("select-layout", "-t", "play:0", "even-horizontal")
	// one heavy neighbor: ~700k lines of wrapping scrollback
	r.T("respawn-pane", "-k", "-t", "play:0.1",
		"sh -c 'seq 700000 | awk \"{print \\$0, \\$0*3, \\\"pad pad pad pad\\\"}\"; exec cat'")
	sleep(6000)
	cp := r.T("display-message", "-p", "-t", "play:0.0", "#{pane_id}")
	r.WaitUntil(30000, func() bool {
		h := r.T("display-message", "-p", "-t", "play:0.1", "#{history_size}")
		return len(h) >= 6 // >=100k
	})
	sleep(2000)
	// fatten claude's input box: a large paste, never submitted
	paste := strings.Repeat("the quick brown fox jumps over the lazy dog and keeps going. ", 120)
	r.T("send-keys", "-t", cp, "-l", paste)
	sleep(1500)
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	r.T("select-pane", "-t", cp)
	sleep(800)

	r.D("toggle", r.CL)
	sleep(1800)
	r.StartRecord()
	r.D("toggle", r.CL) // close from the sidebar, landing in cc
	sleep(1500)
	chunks := r.StopRecordT()
	var all []byte
	var tl []string
	for _, c := range chunks {
		all = append(all, c.Data...)
		if len(c.Data) > 300 {
			er := bytes.Count(c.Data, []byte("\x1b[2K")) + bytes.Count(c.Data, []byte("\x1b[K")) + bytes.Count(c.Data, []byte("\x1b[J"))
			tl = append(tl, fmt.Sprintf("+%dms:%dB(e%d)", c.At.Milliseconds(), len(c.Data), er))
		}
	}
	t.Logf("close: %d frames, %d bytes", bytes.Count(all, []byte("\x1b[?2026h")), len(all))
	t.Logf("timeline: %s", strings.Join(tl, " "))
}
