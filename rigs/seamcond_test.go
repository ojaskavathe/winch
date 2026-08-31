package rigs

import (
	"strings"
	"testing"
)

// TestSeamGroundsAConditionalBorderStyle: catppuccin writes
// pane-active-border-style as a CONDITIONAL, and that is the style painting
// the seam most of the time.
//
// The grounding declined format-valued styles at first — the right instinct
// from the prompt confinement, the wrong call here. tmux expands the whole
// style with format_expand before style_parse sees it, so a ground can simply
// be prepended. Declining meant the normal border got grounded, the ACTIVE one
// did not, and the corner kept its gap for exactly the theme this was for.
func TestSeamGroundsAConditionalBorderStyle(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-position", "top")
	r.T("set-option", "-g", "status-style", "bg=#181825,fg=#cdd6f4")
	r.T("set-option", "-gw", "pane-border-style", "fg=#6c7086")
	r.T("set-option", "-gw", "pane-active-border-style",
		"#{?pane_in_mode,fg=#b4befe,#{?pane_synchronized,fg=#cba6f7,fg=#b4befe}}")
	r.KillDaemon()
	r.D("ls")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	w := r.Side().Win
	sleep(800)

	act := r.ShowOpt("-w", "-t", w, "-v", "pane-active-border-style")
	nrm := r.ShowOpt("-w", "-t", w, "-v", "pane-border-style")
	t.Logf("  normal = %q", nrm)
	t.Logf("  active = %q", act)
	r.Chk("the normal border is grounded", strings.HasPrefix(nrm, "bg=#181825"))
	r.Chk("the CONDITIONAL active border is grounded too", strings.HasPrefix(act, "bg=#181825"))
	r.Chk("and its conditional survives intact", strings.Contains(act, "#{?pane_in_mode,"))

	// Which is what makes the corner join up where it is actually painted.
	sideW := r.Side().Width
	s := statusScreenUntil(r, func(s *screen) bool {
		return s.bg[0][0] != "" && s.bg[0][sideW] != "" && s.grid[1][sideW] == '│'
	})
	t.Logf("  strip bg=%q glyph bg=%q border bg=%q", s.bg[0][0], s.bg[0][sideW], s.bg[1][sideW])
	r.Chk("strip and glyph share a ground", s.bg[0][0] != "" && s.bg[0][0] == s.bg[0][sideW])
	r.Chk("and the border below shares it", s.bg[1][sideW] == s.bg[0][sideW])

	// Given back on undock, conditional and all.
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	after := r.ShowOpt("-w", "-t", w, "-v", "pane-active-border-style")
	r.Chk("restored on undock", !strings.Contains(after, "bg=#181825"))
	if strings.Contains(after, "bg=#181825") {
		t.Logf("  stranded: %q", after)
	}
}
