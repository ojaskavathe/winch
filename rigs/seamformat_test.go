package rigs

import (
	"strings"
	"testing"
)

// TestFormatValuedSeamStyle: @winch-seam-style may be written as a FORMAT, and
// the pad must not shred it.
//
// padCell renders the seam style into a tmux conditional by turning its commas
// into separate #[] directives — tmux splits a conditional at the first comma
// not inside #{}, and does not count #[]. That rewrite is only valid for a
// plain directive list. A conditional's commas are not separators, so
// #{?client_prefix,fg=red,fg=blue} came out as
// #[#{?client_prefix]#[fg=red]#[fg=blue}] and the corner rendered as garbage.
//
// The same class of bug as the command-prompt confinement, one option over: the
// fallback path was safe because resolveStyle expands (catppuccin writes
// pane-active-border-style as a conditional), and only the user's own option
// went in raw.
func TestFormatValuedSeamStyle(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "@winch-seam-style", "#{?pane_in_mode,fg=#f38ba8,fg=#b4befe}")

	// Config is read at attach.
	r.KillDaemon()
	r.D("ls")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(800)

	fmt0 := r.ShowOpt("-t", r.ClientSess(), "-v", "status-format[0]")
	t.Logf("  status-format[0]: %s", fmt0)

	// The giveaway of the shredding: a #[ opened onto a bare #{ fragment.
	r.Chk("the conditional was not split into #[] directives",
		!strings.Contains(fmt0, "#[#{?"))
	// It resolved to one of the two branches instead.
	r.Chk("the seam style resolved to a literal colour",
		strings.Contains(fmt0, "#b4befe") || strings.Contains(fmt0, "#f38ba8"))
	r.Chk("and the border glyph is still there", strings.Contains(fmt0, "│"))

	// The pad still works: the bar is shifted past the sidebar.
	row := statusRow(r, "│")
	r.Chk("the seam glyph lands at the sidebar's border column",
		runeCol(row, "│") == r.Side().Width)
	if got := runeCol(row, "│"); got != r.Side().Width {
		t.Logf("  glyph at col %d, sidebar is %d wide", got, r.Side().Width)
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
