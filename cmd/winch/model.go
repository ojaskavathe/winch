package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The world model. Flat entities with parent refs — clients rebuild the tree.
// All ids are tmux's own ($0 / @1 / %2); indexes shift, ids don't.

type session struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Attached bool   `json:"attached"` // real clients only; control clients excluded
	Created  int64  `json:"created"`

	// Daemon-computed git identity (git.go), injected after every fetch.
	Branch string `json:"branch,omitempty"`
	Ahead  int    `json:"ahead,omitempty"`
	Behind int    `json:"behind,omitempty"`
}

type window struct {
	ID        string `json:"id"`
	SessionID string `json:"session"`
	Index     int    `json:"index"`
	Name      string `json:"name"`
	Active    bool   `json:"active"` // session's current window
	Layout    string `json:"layout"`
}

type pane struct {
	ID        string `json:"id"`
	WindowID  string `json:"window"`
	SessionID string `json:"session"`
	Index     int    `json:"index"`
	Active    bool   `json:"active"` // window's active pane
	// Last is tmux's own pane_last: the pane focus would return to. It is
	// how the sidebar knows where you came from, which matters because
	// docking moves focus ONTO winch — at that moment the agent you were
	// working in is not active anywhere, and Last is the only record of it.
	Last    bool   `json:"last,omitempty"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Command string `json:"cmd"`
	Path    string `json:"path"`
	Title   string `json:"title"`

	// Daemon-computed agent detection (detect.go), injected after every
	// fetch — never read back from tmux.
	Agent       string `json:"agent,omitempty"`   // claude | grok | codex | ...
	AgentState  string `json:"astate,omitempty"`  // working | blocked | idle
	AgentReason string `json:"areason,omitempty"` // blocked only: why ("permission prompt")
	// AgentSeq stamps the last state change, monotonically. Equal-attention
	// agents sort by it, most recent first: among several idle agents the
	// one that just finished is the one being looked for.
	AgentSeq int64 `json:"aseq,omitempty"`
}

type tclient struct {
	Name      string `json:"name"`
	SessionID string `json:"session"`
	// TTY is the terminal on the other side of this client — the device tmux
	// itself writes frames to. It is how a desktop notification gets out of
	// tmux at all (notify.go); nothing renders from it.
	TTY string `json:"tty,omitempty"`
	// Term is the client's TERM, which is the only handle winch has on WHICH
	// terminal is over there — and terminals disagree about which escape
	// sequence means "tell the desktop". Used to pick the dialect per client
	// rather than making the user configure one globally.
	Term string `json:"term,omitempty"`
	// Focused reports whether that terminal currently has the OS focus.
	// Requires `focus-events on`; without it tmux never asks the terminal to
	// report focus and this stays true forever, which degrades to the old
	// behaviour rather than breaking.
	Focused bool `json:"focused,omitempty"`
}

type world struct {
	Sessions []session `json:"sessions"`
	Windows  []window  `json:"windows"`
	Panes    []pane    `json:"panes"`
	Clients  []tclient `json:"clients"`
}

const sep = "\x1f" // unit separator: cannot appear in names/paths/titles sanely

func f(fields ...string) string {
	return "'" + strings.Join(fields, sep) + "'"
}

// fetchWorld re-lists everything over the control connection. Whole-world on
// purpose: a few KBs over a local socket, and it self-heals every gap in the
// notification matrix (cross-session geometry included) at each trigger.
func fetchWorld(c *control) (world, error) {
	var w world

	lines, err := c.run("list-sessions -F " + f("#{session_id}", "#{session_name}", "#{session_created}"))
	if err != nil {
		return w, err
	}
	for _, ln := range lines {
		p := strings.Split(ln, sep)
		if len(p) != 3 {
			continue
		}
		created, _ := strconv.ParseInt(p[2], 10, 64)
		w.Sessions = append(w.Sessions, session{ID: p[0], Name: p[1], Created: created})
	}

	lines, err = c.run("list-windows -a -F " + f("#{session_id}", "#{window_id}", "#{window_index}", "#{window_active}", "#{window_layout}", "#{window_name}"))
	if err != nil {
		return w, err
	}
	for _, ln := range lines {
		p := strings.Split(ln, sep)
		if len(p) != 6 {
			continue
		}
		idx, _ := strconv.Atoi(p[2])
		w.Windows = append(w.Windows, window{
			ID: p[1], SessionID: p[0], Index: idx,
			Active: p[3] == "1", Layout: p[4], Name: p[5],
		})
	}

	lines, err = c.run("list-panes -a -F " + f("#{session_id}", "#{window_id}", "#{pane_id}", "#{pane_index}", "#{pane_active}", "#{pane_width}", "#{pane_height}", "#{pane_current_command}", "#{pane_current_path}", "#{pane_title}", "#{pane_last}"))
	if err != nil {
		return w, err
	}
	for _, ln := range lines {
		p := strings.Split(ln, sep)
		if len(p) != 11 {
			continue
		}
		idx, _ := strconv.Atoi(p[3])
		width, _ := strconv.Atoi(p[5])
		height, _ := strconv.Atoi(p[6])
		_, name := splitOrnament(p[9])
		w.Panes = append(w.Panes, pane{
			ID: p[2], WindowID: p[1], SessionID: p[0], Index: idx,
			Active: p[4] == "1", Last: p[10] == "1", Width: width, Height: height,
			// Title carries no ornament. It re-emits several times a second
			// while an agent works, and diffWorlds compares whole pane
			// structs, so folded in it made every animation frame read as a
			// change to the NAME — which is what the row is keyed on.
			//
			// The ornament is dropped entirely rather than published: the
			// sidebar runs its OWN spinner off a local clock (tui.go), so
			// the agent's frames are not needed to animate — only its state
			// is, and that is AgentState.
			//
			// herdr draws the same line one layer later: TerminalTitleChanges
			// carries raw_changed and stripped_changed separately, and a
			// layout showing the raw title repaints on frames while one
			// showing the stripped title does not.
			Command: p[7], Path: p[8], Title: name,
		})
	}

	lines, err = c.run("list-clients -F " + f("#{client_name}", "#{client_control_mode}", "#{session_id}",
		"#{client_tty}", "#{client_termname}", "#{client_flags}"))
	if err != nil {
		return w, err
	}
	attached := map[string]bool{}
	for _, ln := range lines {
		p := strings.Split(ln, sep)
		if len(p) != 6 || p[1] == "1" {
			continue // skip control clients (including ourselves)
		}
		// tmux carries focus in the flag STRING; there is no #{client_focused}.
		w.Clients = append(w.Clients, tclient{
			Name: p[0], SessionID: p[2], TTY: p[3], Term: p[4],
			Focused: strings.Contains(","+p[5]+",", ",focused,"),
		})
		attached[p[2]] = true
	}
	for i := range w.Sessions {
		w.Sessions[i].Attached = attached[w.Sessions[i].ID]
	}

	sort.Slice(w.Sessions, func(i, j int) bool { return w.Sessions[i].Name < w.Sessions[j].Name })
	sort.Slice(w.Windows, func(i, j int) bool {
		if w.Windows[i].SessionID != w.Windows[j].SessionID {
			return w.Windows[i].SessionID < w.Windows[j].SessionID
		}
		return w.Windows[i].Index < w.Windows[j].Index
	})
	sort.Slice(w.Panes, func(i, j int) bool {
		if w.Panes[i].WindowID != w.Panes[j].WindowID {
			return w.Panes[i].WindowID < w.Panes[j].WindowID
		}
		return w.Panes[i].Index < w.Panes[j].Index
	})
	sort.Slice(w.Clients, func(i, j int) bool { return w.Clients[i].Name < w.Clients[j].Name })
	return w, nil
}

// op is one entry in a diff message: whole-entity put/del, parents before
// children on put, children before parents on del.
type op struct {
	Op    string `json:"op"` // put | del
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Value any    `json:"value,omitempty"`
}

func diffWorlds(old, cur world) []op {
	var ops []op

	oldS := map[string]session{}
	for _, s := range old.Sessions {
		oldS[s.ID] = s
	}
	curS := map[string]bool{}
	for _, s := range cur.Sessions {
		curS[s.ID] = true
		if prev, ok := oldS[s.ID]; !ok || prev != s {
			ops = append(ops, op{Op: "put", Kind: "session", ID: s.ID, Value: s})
		}
	}

	oldW := map[string]window{}
	for _, x := range old.Windows {
		oldW[x.ID] = x
	}
	curW := map[string]bool{}
	for _, x := range cur.Windows {
		curW[x.ID] = true
		if prev, ok := oldW[x.ID]; !ok || prev != x {
			ops = append(ops, op{Op: "put", Kind: "window", ID: x.ID, Value: x})
		}
	}

	oldP := map[string]pane{}
	for _, x := range old.Panes {
		oldP[x.ID] = x
	}
	curP := map[string]bool{}
	for _, x := range cur.Panes {
		curP[x.ID] = true
		if prev, ok := oldP[x.ID]; !ok || prev != x {
			ops = append(ops, op{Op: "put", Kind: "pane", ID: x.ID, Value: x})
		}
	}

	oldC := map[string]tclient{}
	for _, x := range old.Clients {
		oldC[x.Name] = x
	}
	curC := map[string]bool{}
	for _, x := range cur.Clients {
		curC[x.Name] = true
		if prev, ok := oldC[x.Name]; !ok || prev != x {
			ops = append(ops, op{Op: "put", Kind: "client", ID: x.Name, Value: x})
		}
	}

	for _, x := range old.Panes {
		if !curP[x.ID] {
			ops = append(ops, op{Op: "del", Kind: "pane", ID: x.ID})
		}
	}
	for _, x := range old.Windows {
		if !curW[x.ID] {
			ops = append(ops, op{Op: "del", Kind: "window", ID: x.ID})
		}
	}
	for _, x := range old.Sessions {
		if !curS[x.ID] {
			ops = append(ops, op{Op: "del", Kind: "session", ID: x.ID})
		}
	}
	for _, x := range old.Clients {
		if !curC[x.Name] {
			ops = append(ops, op{Op: "del", Kind: "client", ID: x.Name})
		}
	}
	return ops
}

func (w world) String() string {
	var b strings.Builder
	winsBySession := map[string][]window{}
	for _, x := range w.Windows {
		winsBySession[x.SessionID] = append(winsBySession[x.SessionID], x)
	}
	panesByWindow := map[string][]pane{}
	for _, x := range w.Panes {
		panesByWindow[x.WindowID] = append(panesByWindow[x.WindowID], x)
	}
	for _, s := range w.Sessions {
		att := ""
		if s.Attached {
			att = " (attached)"
		}
		fmt.Fprintf(&b, "%s %s%s\n", s.ID, s.Name, att)
		for _, win := range winsBySession[s.ID] {
			mark := " "
			if win.Active {
				mark = "*"
			}
			fmt.Fprintf(&b, "  %s %d:%s%s\n", win.ID, win.Index, win.Name, mark)
			for _, p := range panesByWindow[win.ID] {
				mark := " "
				if p.Active {
					mark = "*"
				}
				fmt.Fprintf(&b, "    %s%s %s [%dx%d] %s\n", p.ID, mark, p.Command, p.Width, p.Height, p.Path)
			}
		}
	}
	return b.String()
}
