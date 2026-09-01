package main

import "testing"

// tmux renames a window after its foreground process, so the raw name is
// "nvim", or ".claude-wrapped" once nix has wrapped it. Both were observed
// live on 2026-09-01 as "winch:.claude-wrapped" and "workbench:nvim" —
// locations that tell you nothing the word "claude" had not already.
func TestNotifyWhereDropsAutoNames(t *testing.T) {
	world := func(winName, cmd string, wins int) *world {
		w := &world{
			Sessions: []session{{ID: "$1", Name: "sr"}},
			Windows:  []window{{ID: "@1", SessionID: "$1", Index: 1, Name: winName}},
			Panes:    []pane{{ID: "%1", WindowID: "@1", SessionID: "$1", Command: cmd}},
		}
		for i := 2; i <= wins; i++ {
			w.Windows = append(w.Windows, window{ID: "@x", SessionID: "$1", Index: i, Name: "other"})
		}
		return w
	}
	for _, tc := range []struct {
		name, winName, cmd string
		wins               int
		want               string
	}{
		// The two seen live. The nix wrapper matters: baseCmd has to strip
		// the "." and "-wrapped" or the name never matches its own command.
		{"nix-wrapped agent", ".claude-wrapped", ".claude-wrapped", 1, "sr"},
		{"editor", "nvim", "nvim", 1, "sr"},
		// Auto-named but the session has siblings: the index disambiguates,
		// which is what the sidebar shows and what herdr's tab_display_name
		// falls back to.
		{"auto-named, many windows", "nvim", "nvim", 3, "sr · 1"},
		// A name the user actually chose survives.
		{"user-named", "review", "nvim", 1, "sr · review"},
		{"user-named, many windows", "review", "nvim", 3, "sr · review"},
		// An unnamed window is auto-named by definition.
		{"no name", "", "nvim", 1, "sr"},
	} {
		if got := notifyWhere(world(tc.winName, tc.cmd, tc.wins), "@1"); got != tc.want {
			t.Errorf("%s: notifyWhere = %q want %q", tc.name, got, tc.want)
		}
	}
	// A window that has gone must not produce a stray separator.
	if got := notifyWhere(world("nvim", "nvim", 1), "@404"); got != "" {
		t.Errorf("missing window: %q", got)
	}
}
