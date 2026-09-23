package main

import (
	"errors"
	"log"
	"strings"
)

// The jumplist: vim's CTRL-O / CTRL-I across tmux windows and sessions.
//
// tmux itself only remembers ONE step back — last-window, last-pane,
// switch-client -l — each a toggle between two places. The daemon already sees
// every focus change on every re-list, so it keeps the real thing: an ordered
// history per client that `winch jump back` / `winch jump fwd` walk.
//
// A jump is a WINDOW change (within a session or across sessions). Moving
// between panes of one window is not a jump, the way j/k are not in vim — it
// only updates which pane the entry returns to, so going back lands on the
// split you left rather than whichever was active when you first arrived.
//
// Semantics are vim's default (not jumpoptions=stack): arriving somewhere new
// moves the entry you came FROM to the end, drops any older copy of the new
// window, and appends it. Going back then jumping keeps the entries after the
// old position, and CTRL-O from there returns to where you just were — which is
// what a jumplist is for. Duplicates never accumulate, so toggling between two
// windows keeps the list two long.
//
// Opt-in with `set -g @winch-jumplist on`, read at attach like every other
// option. Off, nothing is recorded and the commands say how to turn it on; the
// keys themselves are the user's to bind (tmux.conf), never winch's.

const optJumplist = "@winch-jumplist"

// jumpCap bounds a client's history; vim keeps 100.
const jumpCap = 100

type jumpLoc struct {
	sess, win, pane string
}

type jumpList struct {
	locs []jumpLoc
	cur  int // the entry the client is at; meaningless while locs is empty
}

type jumpState struct {
	on    bool
	lists map[string]*jumpList // by client name
}

// arrive records the client standing at loc. Same window as the current entry:
// just refresh it. Anywhere else is a jump.
func (l *jumpList) arrive(loc jumpLoc) {
	if len(l.locs) > 0 && l.locs[l.cur].win == loc.win {
		l.locs[l.cur].sess = loc.sess
		if loc.pane != "" {
			l.locs[l.cur].pane = loc.pane
		}
		return
	}
	if len(l.locs) > 0 {
		from := l.locs[l.cur]
		l.remove(l.cur)
		l.locs = append(l.locs, from)
	}
	for i := 0; i < len(l.locs); i++ {
		if l.locs[i].win == loc.win {
			if loc.pane == "" {
				loc.pane = l.locs[i].pane
			}
			l.remove(i)
			i--
		}
	}
	l.locs = append(l.locs, loc)
	if over := len(l.locs) - jumpCap; over > 0 {
		l.locs = append(l.locs[:0], l.locs[over:]...)
	}
	l.cur = len(l.locs) - 1
}

// remove drops entry i, keeping cur on the same entry (or its predecessor when
// it was the one removed).
func (l *jumpList) remove(i int) {
	l.locs = append(l.locs[:i], l.locs[i+1:]...)
	if l.cur > i || (l.cur == i && l.cur > 0) {
		l.cur--
	}
	if l.cur >= len(l.locs) {
		l.cur = len(l.locs) - 1
	}
	if l.cur < 0 {
		l.cur = 0
	}
}

// prune drops windows that no longer exist and forgets panes that died, so a
// walk never tries to land somewhere gone.
func (l *jumpList) prune(wins map[string]string, panes map[string]string) {
	for i := 0; i < len(l.locs); i++ {
		sid, ok := wins[l.locs[i].win]
		if !ok {
			l.remove(i)
			i--
			continue
		}
		l.locs[i].sess = sid
		if p := l.locs[i].pane; p != "" && panes[p] != l.locs[i].win {
			l.locs[i].pane = ""
		}
	}
}

// step moves cur by dir (-1 back, +1 forward) and returns the entry to go to.
func (l *jumpList) step(dir int) (jumpLoc, bool) {
	next := l.cur + dir
	if len(l.locs) == 0 || next < 0 || next >= len(l.locs) {
		return jumpLoc{}, false
	}
	l.cur = next
	return l.locs[next], true
}

// jumpLocOf is where a client stands in w: its session's active window and that
// window's active pane. winch's own panes (the sidebar, a spacer) are not a
// place to return to, so they leave the pane blank and the entry keeps the pane
// the user was last really in.
func (d *daemon) jumpLocOf(w world, c tclient) (jumpLoc, bool) {
	loc := jumpLoc{sess: c.SessionID}
	for _, x := range w.Windows {
		if x.SessionID == c.SessionID && x.Active {
			loc.win = x.ID
			break
		}
	}
	if loc.win == "" {
		return loc, false
	}
	for _, p := range w.Panes {
		if p.WindowID == loc.win && p.Active {
			if !d.winchPane(p.ID) {
				loc.pane = p.ID
			}
			break
		}
	}
	return loc, true
}

// winchPane reports whether a pane is winch furniture: the docked sidebar or a
// spacer holding its slot in another window.
func (d *daemon) winchPane(pid string) bool {
	p := d.dock
	if p == nil {
		return false
	}
	if pid == p.pane {
		return true
	}
	for _, c := range p.carved {
		if c.spacer == pid {
			return true
		}
	}
	return false
}

// recordJumps runs after every re-list: each client's position goes into its
// list, dead windows and detached clients drop out.
func (d *daemon) recordJumps(w world) {
	if !d.jumps.on {
		return
	}
	if d.jumps.lists == nil {
		d.jumps.lists = map[string]*jumpList{}
	}
	wins := make(map[string]string, len(w.Windows))
	for _, x := range w.Windows {
		wins[x.ID] = x.SessionID
	}
	panes := make(map[string]string, len(w.Panes))
	for _, p := range w.Panes {
		panes[p.ID] = p.WindowID
	}
	seen := map[string]bool{}
	for _, c := range w.Clients {
		if c.Name == "" {
			continue
		}
		seen[c.Name] = true
		l := d.jumps.lists[c.Name]
		if l == nil {
			l = &jumpList{}
			d.jumps.lists[c.Name] = l
		}
		l.prune(wins, panes)
		if loc, ok := d.jumpLocOf(w, c); ok {
			l.arrive(loc)
		}
	}
	for name := range d.jumps.lists {
		if !seen[name] {
			delete(d.jumps.lists, name)
		}
	}
}

// jump is `winch jump back|fwd <client>`: walk the client's history one entry.
//
// The world is re-listed first rather than trusted: a move made a moment ago
// may not have reached a re-list yet (they are debounced), and walking from a
// stale position would skip the place you were just in.
func (d *daemon) jump(ctl *control, client, dir string) (err error) {
	if client == "" {
		return errors.New("jump needs a client name")
	}
	step := 0
	switch dir {
	case "back":
		step = -1
	case "fwd":
		step = 1
	default:
		return errors.New("jump wants back or fwd")
	}
	if !d.jumps.on {
		_, _ = ctl.run("display-message -c " + q(client) + " " +
			q("winch: jumplist is off (set -g "+optJumplist+" on)"))
		return nil
	}
	w, ferr := fetchWorld(ctl)
	if ferr != nil {
		return ferr
	}
	d.recordJumps(w)
	l := d.jumps.lists[client]
	if l == nil {
		return nil
	}
	was := l.cur
	to, ok := l.step(step)
	if !ok {
		return nil
	}
	// A move that fails leaves the client where it was, so the list has to
	// stay there too — otherwise the next re-list reads the unchanged spot as
	// a fresh jump away from the target and reorders the history around it.
	defer func() {
		if err != nil {
			l.cur = was
		}
	}()
	log.Printf("jump %s %s -> %s %s", dir, client, to.win, to.pane)

	// Docked, the sidebar rides along exactly as it does for routed nav; a
	// scrub in flight lands on the target the way a commit would.
	if p := d.dock; p != nil && p.client == client {
		if p.scrubbing {
			err = d.commitScrub(ctl, to.win, to.pane)
			return err
		}
		if err = d.dockMove(ctl, to.win, true, to.pane); err != nil {
			return err
		}
		d.pushSelect(selectMsg{Type: "select", Window: to.win})
		return nil
	}
	cmds := []string{"switch-client -c " + q(client) + " -t " + q(to.sess),
		"select-window -t " + q(to.win)}
	if to.pane != "" {
		cmds = append(cmds, "select-pane -t "+q(to.pane))
	}
	_, err = ctl.runSeq(cmds...)
	return err
}

// loadJumplist reads @winch-jumplist: on/off, default off.
func (d *daemon) loadJumplist(ctl *control) {
	s := strings.ToLower(strings.TrimSpace(optStr(ctl, optJumplist)))
	switch s {
	case "", "off", "0", "false", "no":
		d.jumps.on = false
	case "on", "1", "true", "yes":
		d.jumps.on = true
	default:
		log.Printf("config: %s=%q is not on/off, ignoring", optJumplist, s)
	}
	if !d.jumps.on {
		d.jumps.lists = nil
	}
}
