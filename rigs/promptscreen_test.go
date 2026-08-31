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

// TestCommandPromptStyleGetsTheFillToo: prefix-: is painted from a DIFFERENT
// option than the one that positions it.
//
// status_prompt_redraw picks message-command-style when the prompt mode is
// PROMPT_COMMAND and message-style otherwise, while status_prompt_area reads
// message-style either way. So filling message-style alone gave prefix-: an
// area it never erased — the fix looked right in the option and changed
// nothing on screen, because prefix-: is exactly the command-prompt case.
func TestCommandPromptStyleGetsTheFillToo(t *testing.T) {
	r := New(t)
	r.T("set-option", "-g", "status-style", "bg=#181825,fg=#cdd6f4")
	r.T("set-option", "-g", "message-style", "fg=#94e2d5,bg=default,align=centre")
	r.T("set-option", "-g", "message-command-style", "fg=#94e2d5,bg=default,align=centre")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(800)
	sess := r.ClientSess()

	cs := r.ShowOpt("-t", sess, "-v", "message-command-style")
	t.Logf("  message-command-style while docked: %q", cs)
	r.Chk("the command prompt's own style is filled", strings.Contains(cs, "fill=#181825"))
	r.Chk("their colours survive there too", strings.Contains(cs, "#94e2d5"))
	// Width and align belong to message-style alone — status_prompt_area reads
	// them from there — so putting them here would be dead weight that still
	// has to be saved and restored.
	r.Chk("no width= on the command style", !strings.Contains(cs, "width="))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	after := r.ShowOpt("-t", sess, "-v", "message-command-style")
	r.Chk("message-command-style restored on undock", !strings.Contains(after, "fill="))
	if strings.Contains(after, "fill=") {
		t.Logf("  stranded: %q", after)
	}
}

// TestFormatValuedMessageStyleStillDocks: somebody else's theme is allowed to
// write a CONDITIONAL message-style, and winch must not choke on it.
//
// Every edit winch makes to a style splits on commas, and a format's commas are
// not separators — `#{?pane_in_mode,fg=red,fg=blue}` is one directive holding
// two. Splitting it yields fragments that are not styles. Worse than looking
// wrong: set-option validates styles (options.c, "invalid style") and tmux
// aborts a sequence at the first error, and these options ride in the SAME
// batch as the dock — so a mangled style would stop the sidebar opening at all.
//
// winch declines the confinement instead. Those users get the prompt painting
// over the sidebar, which is where everyone was before confinement existed.
func TestFormatValuedMessageStyleStillDocks(t *testing.T) {
	r := New(t)
	cond := "#{?client_prefix,fg=red,fg=green},bg=default"
	r.T("set-option", "-g", "message-style", cond)
	r.T("set-option", "-g", "message-command-style", cond)

	r.D("toggle", r.CL)
	r.Chk("the sidebar still docks", r.WaitUntil(6000, func() bool { return r.Side().Pane != "" }))
	sleep(800)
	r.Chk("and it is a real sidebar, at its proper width", r.Side().Width == sideW)

	// Read at SESSION scope: `show-options -v` does not inherit, so empty here
	// means winch wrote nothing of its own onto the session — which is the
	// claim. The global is what the user set and must be untouched.
	sess := r.ClientSess()
	ms := r.ShowOpt("-t", sess, "-v", "message-style")
	cs := r.ShowOpt("-t", sess, "-v", "message-command-style")
	t.Logf("  session-scope message-style:         %q", ms)
	t.Logf("  session-scope message-command-style: %q", cs)
	r.Chk("winch claimed nothing on the session", ms == "" && cs == "")
	r.Chk("the user's format is still theirs", r.ShowOpt("-g", "-v", "message-style") == cond)
	r.Chk("and so is the command style", r.ShowOpt("-g", "-v", "message-command-style") == cond)

	// The rest of the dock is unaffected — the status pad still installed,
	// which is what a batch aborted by an invalid style would have taken down.
	r.Chk("the status pad still went in",
		strings.Contains(r.ShowOpt("-t", sess, "-v", "status-format[0]"), "#[range"))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	r.Chk("style untouched after undock too",
		r.ShowOpt("-g", "-v", "message-style") == cond)
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
