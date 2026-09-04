package rigs

import (
	"strings"
	"testing"
)

// TestRenameKeepsOrder: a rename must NOT reorder. The order is stored by name,
// so the daemon has to remap @winch-session-order when a session is renamed —
// otherwise the renamed session falls out of the pinned order and drops to the
// creation-order tail. The rig pins [play, work] (play on top); play was
// created AFTER work, so a broken remap would drop the renamed play BELOW work.
func TestRenameKeepsOrder(t *testing.T) {
	r := New(t)
	r.D("toggle", r.CL)
	sleep(900)
	sp := r.Side().Pane

	r.Chk("play starts on top (pinned order)", r.WaitUntil(3000, func() bool {
		c := r.Capture(sp)
		ip, iw := strings.Index(c, "play"), strings.Index(c, "work")
		return ip >= 0 && iw >= 0 && ip < iw
	}))

	// Rename play -> ploy (external tmux rename: exercises the world-diff path
	// that catches every rename, not just the sidebar's own).
	r.T("rename-session", "-t", "play", "ploy")

	r.Chk("renamed session keeps its top spot", r.WaitUntil(5000, func() bool {
		c := r.Capture(sp)
		ip, iw := strings.Index(c, "ploy"), strings.Index(c, "work")
		return ip >= 0 && iw >= 0 && ip < iw && !strings.Contains(c, "play")
	}))

	r.Chk("order option remapped to the new name", r.WaitUntil(3000, func() bool {
		o := r.ShowOpt("-gv", "@winch-session-order")
		ip, iw := strings.Index(o, "ploy"), strings.Index(o, "work")
		return ip >= 0 && iw >= 0 && ip < iw && !strings.Contains(o, `"play"`)
	}))

	r.Undock()
	sleep(500)
}
