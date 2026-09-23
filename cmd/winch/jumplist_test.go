package main

import (
	"reflect"
	"testing"
)

func wins(l *jumpList) []string {
	var out []string
	for _, x := range l.locs {
		out = append(out, x.win)
	}
	return out
}

func visit(l *jumpList, ws ...string) {
	for _, w := range ws {
		l.arrive(jumpLoc{sess: "$0", win: w, pane: "%" + w[1:]})
	}
}

// TestJumpBackForward: the plain walk — back through history and forward
// again, stopping quietly at either end.
func TestJumpBackForward(t *testing.T) {
	l := &jumpList{}
	visit(l, "@1", "@2", "@3")
	for _, want := range []string{"@2", "@1"} {
		to, ok := l.step(-1)
		if !ok || to.win != want {
			t.Fatalf("back: got %v %v, want %s", to.win, ok, want)
		}
		l.arrive(to) // the re-list after the move sees the client there
	}
	if _, ok := l.step(-1); ok {
		t.Fatal("back past the oldest entry should do nothing")
	}
	for _, want := range []string{"@2", "@3"} {
		to, ok := l.step(1)
		if !ok || to.win != want {
			t.Fatalf("fwd: got %v %v, want %s", to.win, ok, want)
		}
		l.arrive(to)
	}
	if _, ok := l.step(1); ok {
		t.Fatal("fwd past the newest entry should do nothing")
	}
}

// TestJumpFromMiddleIsVimDefault: going back and then jumping somewhere new
// keeps the later entries and moves the one you left to the end, so CTRL-O
// returns to where you just were rather than into the old tail.
func TestJumpFromMiddleIsVimDefault(t *testing.T) {
	l := &jumpList{}
	visit(l, "@1", "@2", "@3", "@4")
	l.step(-1)
	l.step(-1) // at @2
	visit(l, "@5")
	if got, want := wins(l), []string{"@1", "@3", "@4", "@2", "@5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list %v, want %v", got, want)
	}
	if to, _ := l.step(-1); to.win != "@2" {
		t.Fatalf("back went to %s, want @2 (where you jumped from)", to.win)
	}
}

// TestJumpNoDuplicates: toggling between two windows never grows the list.
func TestJumpNoDuplicates(t *testing.T) {
	l := &jumpList{}
	visit(l, "@1", "@2", "@1", "@2", "@1")
	if got, want := wins(l), []string{"@2", "@1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list %v, want %v", got, want)
	}
}

// TestJumpPaneMoveIsNotAJump: moving between splits of one window only
// updates where that entry returns to; a blank pane (the sidebar) keeps it.
func TestJumpPaneMoveIsNotAJump(t *testing.T) {
	l := &jumpList{}
	visit(l, "@1", "@2")
	l.arrive(jumpLoc{sess: "$0", win: "@2", pane: "%9"})
	l.arrive(jumpLoc{sess: "$0", win: "@2", pane: ""})
	if got := wins(l); len(got) != 2 {
		t.Fatalf("pane moves added entries: %v", got)
	}
	l.step(-1)
	if to, _ := l.step(1); to.pane != "%9" {
		t.Fatalf("returned to pane %q, want the last real one %%9", to.pane)
	}
}

// TestJumpPrune: closed windows drop out, a dead pane falls back to the
// window's own active pane, and a window moved to another session follows.
func TestJumpPrune(t *testing.T) {
	l := &jumpList{}
	visit(l, "@1", "@2", "@3")
	l.prune(map[string]string{"@1": "$0", "@3": "$7"}, map[string]string{"%1": "@1"})
	if got, want := wins(l), []string{"@1", "@3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list %v, want %v", got, want)
	}
	if l.locs[l.cur].win != "@3" {
		t.Fatalf("cur on %s, want @3", l.locs[l.cur].win)
	}
	if l.locs[1].pane != "" || l.locs[1].sess != "$7" {
		t.Fatalf("@3 entry %+v: want pane cleared, sess $7", l.locs[1])
	}
	if to, _ := l.step(-1); to.win != "@1" || to.pane != "%1" {
		t.Fatalf("back to %+v, want @1 %%1", to)
	}
}

// TestJumpCap: history is bounded.
func TestJumpCap(t *testing.T) {
	l := &jumpList{}
	for i := 0; i < jumpCap+20; i++ {
		l.arrive(jumpLoc{win: "@" + string(rune('A'+i%26)) + string(rune('0'+i/26))})
	}
	if len(l.locs) != jumpCap || l.cur != jumpCap-1 {
		t.Fatalf("len %d cur %d, want %d", len(l.locs), l.cur, jumpCap)
	}
}
