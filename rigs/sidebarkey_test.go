package rigs

import "testing"

// TestMsIsContextual: M-s has three outcomes, not two.
//
//	closed                -> open (and land in it)
//	open, keyboard away   -> focus it
//	open, keyboard in it  -> close
//
// JetBrains tool-window semantics. The middle state is the one that did not
// exist: M-s used to close from anywhere, so getting back INTO an open
// sidebar meant walking left with C-h, one pane per press — several presses
// from the far edge of a split, which is where the whole thing came from.
//
// So the test deliberately parks the keyboard in a pane that is NOT adjacent
// to the sidebar. A select-pane -L implementation passes an adjacent-pane
// test and fails this one.
func TestMsIsContextual(t *testing.T) {
	r := New(t)

	// Three content panes across beta, so the far one is two hops from the
	// sidebar. Splitting before docking keeps the dock's own geometry out
	// of the setup.
	r.T("split-window", "-h", "-t", r.W2)
	far := r.T("split-window", "-h", "-P", "-F", "#{pane_id}", "-t", r.W2)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	side := r.Side().Pane
	r.Chk("opening lands in the sidebar", r.WaitUntil(1000, func() bool {
		return r.ClientPane() == side
	}))

	// Keyboard to the far edge: two panes away from the sidebar.
	r.T("select-pane", "-t", far)
	r.Chk("keyboard parked on the far pane", r.WaitUntil(5000, func() bool {
		return r.ClientPane() == far
	}))

	// One press, from two hops away.
	r.D("toggle", r.CL)
	r.Chk("M-s from a content pane focuses the sidebar", r.WaitUntil(2000, func() bool {
		return r.ClientPane() == side
	}))
	r.Chk("focusing did not close it", r.Side().Pane != "")

	// And now it closes, because the keyboard is in it.
	r.D("toggle", r.CL)
	r.Chk("M-s from inside the sidebar closes it", r.WaitUntil(2000, func() bool {
		return r.WinchPanes("-a") == 0
	}))
}

// M-a is M-s once the sidebar is up: same three states, so the pair never
// disagree about what a press does. Only the opening selection differs.
func TestMaMatchesMsWhenDocked(t *testing.T) {
	r := New(t)

	content := r.T("split-window", "-h", "-P", "-F", "#{pane_id}", "-t", r.W2)

	r.D("toggle", r.CL)
	r.await(5000, "docked", func() bool { return r.Side().Pane != "" })
	side := r.Side().Pane

	r.T("select-pane", "-t", content)
	r.await(2000, "keyboard on content", func() bool { return r.ClientPane() == content })

	// No agents exist in this rig. That must not stop M-a from acting as
	// the sidebar key once the sidebar is already up — the "no agents"
	// refusal belongs to OPENING, not to focusing something already open.
	r.D("agents", r.CL)
	r.Chk("M-a focuses the open sidebar", r.WaitUntil(2000, func() bool {
		return r.ClientPane() == side
	}))
	r.Chk("M-a did not close it", r.Side().Pane != "")

	r.D("agents", r.CL)
	r.Chk("M-a from inside the sidebar closes it", r.WaitUntil(2000, func() bool {
		return r.WinchPanes("-a") == 0
	}))
}
