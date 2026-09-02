package rigs

import (
	"bytes"
	"testing"
)

// TestToggleTint records the client stream through a dock open. The frames
// between the split landing and the TUI's first paint show the sidebar strip
// as an empty pane; untinted, that presents in the terminal's default
// background — a dark flash on every open (probe-measured: split at ~+20ms,
// TUI content at ~+40ms, wider under load). dockOpen tints the pane the
// sidebar's own ground (seamGround) in the split batch, so every pre-paint
// frame shows a themed strip; the TUI's hello lifts the tint so billboard
// default-background cells render normally again.
func TestToggleTint(t *testing.T) {
	r := NewLive(t)
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(300)

	// seamGround as tmux paints it: SGR 48;2;24;24;37
	tint := []byte("48:2:24:24:37")
	tintSemi := []byte("48;2;24;24;37")
	for i := 0; i < 3; i++ {
		r.StartRecord()
		r.D("toggle", r.CL) // open
		sleep(600)
		rec := r.StopRecord()
		first := rec
		if j := bytes.Index(rec, []byte("\x1b[?2026l")); j >= 0 {
			first = rec[:j] // the split frame, before the TUI's first paint
		}
		if !bytes.Contains(first, tint) && !bytes.Contains(first, tintSemi) {
			t.Errorf("cycle %d: split frame presented the strip untinted", i)
		}
		r.D("toggle", r.CL) // focuses sidebar (opened from content pane) or closes
		sleep(200)
		if r.Side().Pane != "" {
			r.D("toggle", r.CL)
			sleep(200)
		}
		sleep(300)
	}
}
