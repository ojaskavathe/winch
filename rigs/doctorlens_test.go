package rigs

import (
	"strings"
	"testing"
)

// TestDoctorAgentLens proves the doctor's "agents" section renders each card
// exactly as the TUI paints it — so a title-vs-render divergence is visible.
// A fake claude agent is given the pane title "claude · resume" (what claude's
// --resume picker sets); the lens must show it rendered WHOLE. Against the
// pre-fix render this FAILS, showing the sliced "claude" beside the intact
// raw title — which is the point.
func TestDoctorAgentLens(t *testing.T) {
	r := New(t)
	// The fake agent is a bare sleep and never re-asserts an OSC title, so
	// stop tmux from reverting the pane title we set.
	r.T("set-option", "-g", "allow-rename", "off")
	r.T("set-window-option", "-g", "automatic-rename", "off")
	fake := buildFakeAgent(t)
	ap := r.T("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", r.W3, fake+" 100000")
	sleep(1700)
	r.AgentUp(ap, "claude · resume")
	r.Settle()
	// Re-assert right before the read, and confirm tmux still holds it.
	r.T("select-pane", "-T", "claude · resume", "-t", ap)
	r.await(3000, "title pinned", func() bool {
		out, _ := r.TQ("display-message", "-p", "-t", ap, "#{pane_title}")
		return strings.TrimSpace(out) == "claude · resume"
	})

	doc := r.D("doctor")
	var sec []string
	in := false
	for _, ln := range strings.Split(doc, "\n") {
		if strings.HasPrefix(ln, "agents") {
			in = true
			continue
		}
		if in && strings.HasPrefix(ln, "status line") {
			break
		}
		if in && strings.Contains(ln, ap) || (in && len(sec) > 0 && strings.HasPrefix(ln, "      ")) {
			sec = append(sec, ln)
		}
	}
	block := strings.Join(sec, "\n")
	t.Logf("doctor agents section for %s:\n%s", ap, block)

	if !strings.Contains(block, `title="claude · resume"`) {
		t.Fatalf("lens did not report the raw title:\n%s", block)
	}
	if !strings.Contains(block, `card  "claude · resume"`) {
		t.Errorf("LENS CAUGHT THE BUG: card diverges from raw title (expected a card %q):\n%s",
			"claude · resume", block)
	}
}
