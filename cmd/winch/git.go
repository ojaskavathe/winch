package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Session git identity — herdr's spaces row two (branch, ↑ahead ↓behind).
// Polled on its own slow ticker, NEVER on the event path: these execs are
// the daemon's only process spawns besides tmux itself, and both commands
// answer from refs without touching the worktree.

const gitTick = 5 * time.Second

type gitInfo struct {
	branch        string
	ahead, behind int
}

// gitScan probes each session's repo (via its active pane's cwd) and
// refreshes the cache; reports whether anything changed.
func (d *daemon) gitScan(w *world) bool {
	awin := map[string]bool{}
	for _, win := range w.Windows {
		if win.Active {
			awin[win.ID] = true
		}
	}
	paths := map[string]string{}
	for _, p := range w.Panes {
		if p.Active && awin[p.WindowID] {
			paths[p.SessionID] = p.Path
		}
	}
	next := make(map[string]gitInfo, len(paths))
	changed := len(paths) != len(d.git)
	for sid, path := range paths {
		gi := gitProbe(path)
		next[sid] = gi
		if d.git[sid] != gi {
			changed = true
		}
	}
	d.git = next
	return changed
}

func gitProbe(dir string) gitInfo {
	if dir == "" {
		return gitInfo{}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return gitInfo{} // not a repo (or no git): no row
	}
	gi := gitInfo{branch: strings.TrimSpace(string(out))}
	if ab, err := exec.Command("git", "-C", dir, "rev-list", "--left-right", "--count", "@{upstream}...HEAD").Output(); err == nil {
		fmt.Sscanf(strings.TrimSpace(string(ab)), "%d %d", &gi.behind, &gi.ahead)
	}
	return gi
}

// injectGit copies the cache onto a freshly fetched world, like
// injectAgents — world diffs carry git fields without git owning re-lists.
func (d *daemon) injectGit(w *world) {
	for i := range w.Sessions {
		if gi, ok := d.git[w.Sessions[i].ID]; ok {
			w.Sessions[i].Branch = gi.branch
			w.Sessions[i].Ahead = gi.ahead
			w.Sessions[i].Behind = gi.behind
		}
	}
}
