package rigs

import (
	"strings"
	"testing"
)

// TestPromptAreaClearsItself: confining the command prompt to the right of the
// sidebar sets the AREA but does not clear it, and the difference is visible.
//
// status_prompt_redraw builds the prompt screen by fast-copying the real status
// bar and then drawing the message inside [ax, ax+aw). Nothing after that
// touches the rest of the area: format_draw blanks it only when the style
// carries a `fill=` (format-draw.c, "Clear the available area", guarded on
// sy.fill != 8), and the sub-screens it does clear are only as wide as the
// message itself. A `bg=` does not do it.
//
// Unconfined the consequence is invisible — the area is the whole bar, so the
// stale content sits under a prompt that spans everything. Confined to the
// right of the sidebar it is not: prefix-: left the old status text on screen
// beside the prompt instead of replacing the bar, which is what "ours is
// overwriting the statusbar" describes.
//
// ---- On why this is asserted at the option level ----
//
// Rig.Type CAN open a real prompt (the prompt reads keys from the client, which
// is what Type writes to — send-keys cannot reach it, and `tmux command-prompt`
// from the CLI blocks until answered and hangs the rig). Reading the RESULT is
// the part that does not work. Every way of sampling the bar perturbs it:
// refresh-client, which is the only way to force the status to repaint into the
// recording, makes tmux re-copy the live bar under the prompt — so the
// underlying content appears in the capture whether or not it is on the user's
// screen. A single frame is worse: it carries only cells that CHANGED, so
// "cleared" and "untouched" are the same empty row. Both instruments were built
// and both gave answers that flipped between runs.
//
// So the claim rests on the option plus status.c, as TestPromptStaysPastTheSidebar
// already does for the area itself. Left here rather than deleted because the
// next person to reach for a screen assertion should know it was tried.
func TestPromptAreaClearsItself(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-style", "bg=#181825,fg=#cdd6f4")
	r.T("set-option", "-g", "message-style", "fg=#94e2d5,bg=default,align=centre")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(800)
	sess := r.ClientSess()

	ms := r.ShowOpt("-t", sess, "-v", "message-style")
	t.Logf("  docked message-style: %q", ms)
	r.Chk("the confined area is told to clear itself", strings.Contains(ms, "fill="))
	// bg=default names no colour — tmux spells "no fill" as that same value —
	// so the bar's own background stands in. Erasing to the bar's colour is
	// what an emptied status line looks like.
	r.Chk("filled with the status bar's colour, since their message bg is default",
		strings.Contains(ms, "fill=#181825"))
	r.Chk("their own colours still survive", strings.Contains(ms, "#94e2d5"))

	// A real prompt still has to open and accept input over the top of it —
	// the fill must not have produced a style tmux rejects, which would leave
	// the prompt unusable in a way no option assertion would notice.
	r.Type("\x02:")
	sleep(700)
	r.Type("display-message hello")
	sleep(400)
	r.Chk("a real prompt opens and takes input", r.WaitUntil(3000, func() bool {
		s := statusScreenUntil(r, func(*screen) bool { return false })
		return strings.Contains(string(s.grid[r.prof.rows-1]), "display-message hello")
	}))
	r.Type("\x1b")
	sleep(400)

	// The fill is winch's, so it goes back with everything else. A stranded
	// fill= would repaint the user's prompt forever.
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	after := r.ShowOpt("-t", sess, "-v", "message-style")
	r.Chk("no fill= stranded on undock", !strings.Contains(after, "fill="))
	if strings.Contains(after, "fill=") {
		t.Logf("  stranded: %q", after)
	}
}

// The fill comes from the user's own message background when they picked one,
// and only falls back to the bar's when they did not.
func TestPromptFillPrefersTheUsersOwnBackground(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-style", "bg=#181825,fg=#cdd6f4")
	r.T("set-option", "-g", "message-style", "fg=black,bg=#f9e2af")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(800)

	ms := r.ShowOpt("-t", r.ClientSess(), "-v", "message-style")
	t.Logf("  %q", ms)
	r.Chk("fills with the user's own message background", strings.Contains(ms, "fill=#f9e2af"))
	r.Chk("not the status bar's", !strings.Contains(ms, "fill=#181825"))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
