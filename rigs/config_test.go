package rigs

import (
	"strings"
	"testing"
)

// TestConfigPersists: user-tuned sidebar geometry lives in tmux global user
// options, not daemon memory. Before this, the dragged width died with the
// daemon — so every deploy (pkill + restart) silently reset it, and there was
// no way to preset a preferred width at all.
//
// Three properties, in the order they matter:
//   - a width preset before the daemon exists is honoured on the first dock
//     (this is what makes tmux.conf a valid place to configure winch);
//   - a width adopted from a border drag is written back to the option;
//   - and it survives the daemon being killed and respawned.
func TestConfigPersists(t *testing.T) {
	r := New(t)

	// Preset, the tmux.conf case: set the option, then force a daemon that
	// has never seen a dock to read it at attach.
	r.T("set-option", "-g", "@winch-width", "34")
	r.KillDaemon()
	r.D("ls")
	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	r.Chk("preset @winch-width honoured on first dock", r.Side().Width == 34)

	// Border drag: tmux resizes the pane, the TUI's SIGWINCH reports the new
	// width, and the daemon adopts it because the WINDOW width didn't change
	// (that is how a drag is told from a client resize). The option must
	// follow — the daemon's memory is no longer the only copy.
	r.T("resize-pane", "-t", r.Side().Pane, "-x", "30")
	r.await(5000, "drag adopted", func() bool {
		return strings.TrimSpace(r.ShowOpt("-gqv", "@winch-width")) == "30"
	})
	r.Chk("drag persisted to @winch-width", strings.TrimSpace(r.ShowOpt("-gqv", "@winch-width")) == "30")

	// Undock cleanly, then kill the daemon the way a deploy does.
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	r.KillDaemon()
	r.D("ls")

	r.D("toggle", r.CL)
	r.await(5000, "re-docked", func() bool { return r.Side().Pane != "" })
	r.Chk("width survived the daemon restart", r.Side().Width == 30)
	if w := r.Side().Width; w != 30 {
		t.Logf("  sidebar came back at %d, want 30", w)
	}

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}

// TestConfigRejectsGarbage: these options are hand-editable in tmux.conf, so
// a nonsense value must be ignored rather than rendering a 4-column sidebar.
// Bounds are enforced on read, not just on the drag that writes them.
func TestConfigRejectsGarbage(t *testing.T) {
	r := New(t)

	for _, v := range []string{"4", "999", "notanumber"} {
		r.T("set-option", "-g", "@winch-width", v)
		r.KillDaemon()
		r.D("ls")
		r.D("toggle", r.CL)
		r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
		got := r.Side().Width
		r.Chk("@winch-width="+v+" ignored, default kept", got == sideW)
		if got != sideW {
			t.Logf("  %q produced width %d, want the %d default", v, got, sideW)
		}
		r.Undock()
		r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	}
}
