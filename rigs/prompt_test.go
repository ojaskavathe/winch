package rigs

import (
	"strconv"
	"strings"
	"testing"
)

// TestPromptStaysPastTheSidebar: prefix-: opens tmux's command prompt, and the
// prompt used to paint from column 0 — straight over the sidebar's strip and
// the seam.
//
// It does not have to. status.c's status_prompt_redraw copies the real status
// screen first (screen_write_fast_copy) and only then draws the message, inside
// an area whose x and width come from status_prompt_area() — which reads them
// off message-style's `width=` and `align=`. So confining the area to the right
// of the sidebar leaves everything left of it intact underneath.
//
// Asserted at the option level rather than on pixels: what winch controls is
// the area, and tmux's honouring of it is its own (source-verified) business.
// The screen-level claim that the pad survives is below.
func TestPromptStaysPastTheSidebar(t *testing.T) {
	r := New(t)

	r.T("set-option", "-g", "message-style", "fg=#94e2d5,bg=default,align=centre")

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(700)
	sess := r.ClientSess()
	w := r.Side().Width

	ms := r.ShowOpt("-t", sess, "-v", "message-style")
	t.Logf("  message-style while docked: %q", ms)
	r.Chk("the prompt area is right-aligned", strings.Contains(ms, "align=right"))
	r.Chk("the user's own colours survive", strings.Contains(ms, "#94e2d5"))
	r.Chk("their align=centre was dropped, not left to fight ours",
		!strings.Contains(ms, "align=centre"))

	// The area must start in the first column past the border: x = sx - width,
	// so width = sx - sidebar - 1.
	wantW := r.prof.cols - w - 1
	r.Chk("width leaves exactly the sidebar and its border",
		strings.Contains(ms, "width="+strconv.Itoa(wantW)))
	if !strings.Contains(ms, "width="+strconv.Itoa(wantW)) {
		t.Logf("  want width=%d (cols %d - sidebar %d - 1), got %q", wantW, r.prof.cols, w, ms)
	}

	// And it is the user's again on undock — a themed message-style is theirs,
	// and a stranded width= would confine their prompt forever.
	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
	after := r.ShowOpt("-t", sess, "-v", "message-style")
	r.Chk("message-style restored on undock",
		!strings.Contains(after, "width=") && !strings.Contains(after, "align=right"))
	if strings.Contains(after, "width=") {
		t.Logf("  stranded on %s: %q", sess, after)
	}
}

// There is deliberately no screen-level test of an OPEN prompt, and the reason
// is worth writing down so nobody spends an afternoon rediscovering it: the
// prompt reads keys from the CLIENT, not from a pane, so send-keys cannot reach
// it, and invoking `tmux command-prompt` from the CLI blocks that process until
// the prompt is answered — which never happens, so the rig hangs rather than
// fails. Tried; it hung.
//
// The claim therefore rests on the option assertions above plus status.c, which
// is unusually explicit: status_prompt_redraw copies the whole status screen
// with screen_write_fast_copy before format_draw touches only [ax, ax+aw), and
// ax/aw come from status_prompt_area() reading message-style. Nothing else in
// that function writes outside the area.

// TestPromptConfinementFollowsTheWidth: the area is a literal column count —
// status_prompt_area calls options_string_to_style with a NULL format tree, so
// #{client_width} would never expand — which means every input to it has to be
// re-derived whenever it moves. Dragging the sidebar is the cheapest of those
// to provoke.
func TestPromptConfinementFollowsTheWidth(t *testing.T) {
	r := New(t)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	sleep(700)
	sess := r.ClientSess()

	before := r.ShowOpt("-t", sess, "-v", "message-style")
	w0 := r.Side().Width

	// Widen it the way the user does: drag the pane border. checkDock reads an
	// unchanged WINDOW width as "the user chose this" and adopts it.
	r.T("resize-pane", "-t", r.Side().Pane, "-x", strconv.Itoa(w0+10))
	r.await(5000, "sidebar widened", func() bool { return r.Side().Width == w0+10 })
	sleep(500)

	after := r.ShowOpt("-t", sess, "-v", "message-style")
	t.Logf("  %d cols: %q\n  %d cols: %q", w0, before, w0+10, after)
	r.Chk("the confinement moved with the sidebar", before != after)
	r.Chk("and still matches the new width",
		strings.Contains(after, "width="+strconv.Itoa(r.prof.cols-(w0+10)-1)))

	r.Undock()
	r.await(5000, "undocked", func() bool { return r.WinchPanes("-a") == 0 })
}
