package rigs

import (
	"bytes"
	"fmt"
	"testing"
)

// Probe: what does the CONTENT pane visibly go through when the sidebar
// opens? The pane runs a shell that repaints a full screen of marked lines
// and re-repaints (with a bumped generation) on every SIGWINCH — a stand-in
// for a claude/TUI pane. The recording shows each presented state: tmux's
// own rewrapped frame, the app's repaint(s), and how far apart they land.
func TestPaneFlickerProbe(t *testing.T) {
	r := NewLive(t)
	pane := r.T("display-message", "-p", "-t", "play:0", "#{pane_id}")
	// repaint() clears and prints 20 numbered full-width-ish lines tagged
	// with a generation; trap WINCH bumps the generation and repaints.
	r.SendKeys(pane, `G=0; repaint() { printf '\033[2J\033[H'; for i in $(seq 1 20); do printf 'GEN%d line %02d %s\n' $G $i aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; done; }; trap 'G=$((G+1)); repaint' WINCH; repaint`, "Enter")
	r.T("switch-client", "-c", r.CL, "-t", "play", ";", "select-window", "-t", r.P1)
	sleep(500)

	for cycle := 0; cycle < 3; cycle++ {
		r.StartRecord()
		r.D("toggle", r.CL) // open
		sleep(900)
		chunks := r.StopRecordT()
		var all []byte
		for _, c := range chunks {
			all = append(all, c.Data...)
		}
		parts := bytes.Split(all, []byte("\x1b[?2026h"))
		sum := ""
		for bi, p := range parts {
			if len(p) == 0 {
				continue
			}
			gens := map[string]int{}
			for g := 0; g < 9; g++ {
				tag := []byte(fmt.Sprintf("GEN%d", g))
				if n := bytes.Count(p, tag); n > 0 {
					gens[string(tag)] = n
				}
			}
			sum += fmt.Sprintf(" b%d:%dB %v", bi, len(p), gens)
		}
		var tl string
		for _, c := range chunks {
			tl += fmt.Sprintf(" +%d:%d", c.At.Milliseconds(), len(c.Data))
		}
		t.Logf("open %d bundles:%s\n    chunks:%s", cycle, sum, tl)
		// close for next cycle
		r.D("toggle", r.CL)
		sleep(300)
		if r.Side().Pane != "" {
			r.D("toggle", r.CL)
		}
		sleep(500)
	}
}
