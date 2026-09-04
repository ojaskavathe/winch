package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The command router: client cmds arrive on the hub queue, get coalesced by
// handleCmd, and runCmd dispatches each. Everything is docked-mode now —
// `winch browse` is dock + zoom — so the dock owns every command. All of it
// runs on the consume loop — one thread, serialized with re-lists and tmux
// commands.

// handleCmd drains everything already queued and executes only what still
// matters: every non-preview command in order, the NEWEST real preview, and
// the prefetches that came after it (they describe the final position).
// Stale previews and stale prefetches are acked without running — during a
// fast scrub they'd otherwise serialize ahead of the frame the user is
// actually looking at. Remaining prefetches are abandoned the moment fresher
// input arrives.
func (d *daemon) handleCmd(ctl *control, env cmdEnvelope) {
	batch := []cmdEnvelope{env}
	for {
		select {
		case next := <-d.h.cmds:
			batch = append(batch, next)
			continue
		default:
		}
		break
	}
	lastReal := -1
	for i, e := range batch {
		if e.msg.Cmd == "preview" && !e.msg.Prefetch {
			lastReal = i
		}
	}
	for i, e := range batch {
		isPreview := e.msg.Cmd == "preview"
		switch {
		case !isPreview:
			d.runCmd(ctl, e)
		case !e.msg.Prefetch && i == lastReal:
			d.runCmd(ctl, e)
		case e.msg.Prefetch && i > lastReal:
			if len(d.h.cmds) > 0 {
				// Fresher input queued: this prefetch is already history.
				d.h.send(e.sub, marshalLine(replyMsg{Type: "reply", OK: true}))
				continue
			}
			d.runCmd(ctl, e)
		default:
			d.h.send(e.sub, marshalLine(replyMsg{Type: "reply", OK: true}))
		}
	}
}

func (d *daemon) runCmd(ctl *control, env cmdEnvelope) {
	start := time.Now()
	var err error
	switch env.msg.Cmd {
	case "toggle":
		err = d.toggle(ctl, env.msg.Client)
	case "browse":
		err = d.browseOpen(ctl, env.msg.Client)
	case "agents":
		err = d.agentsOpen(ctl, env.msg.Client)
	case "nav":
		err = d.dockNav(ctl, env.msg.Dir)
	case "equalize":
		err = d.dockEqualize(ctl)
	case "preview":
		// Selection leaving the real window starts billboard scrubbing
		// (zoom + captures); while scrubbing, previews and prefetches are
		// plain billboards. Nothing real moves until commit.
		if p := d.dock; p != nil {
			switch {
			case p.scrubbing:
				if !env.msg.Prefetch && env.msg.Window != "" && env.msg.Window != d.pv.target {
					d.scrubStatusSet(ctl, env.msg.Window)
				}
				err = d.preview(ctl, env.msg.Window, env.msg.Prefetch, false)
			case !env.msg.Prefetch && env.msg.Window != "" && env.msg.Window != p.win:
				err = d.scrubStart(ctl, env.msg.Window)
			}
		}
	case "winch":
		// The TUI's pane changed size. Docked idle that means a client
		// resize (monitor switch) rescaled the sidebar off its fixed width;
		// nothing else will tell us — geometry events don't cross sessions.
		// Zoomed (scrubbing) the sidebar is full-width by design: skip.
		if d.dock != nil && !d.dock.scrubbing && env.msg.Width != d.width() {
			// Drag or client resize? The window width tells: unchanged
			// means the user dragged the pane border — adopt it.
			winW := 0
			if lines, e := ctl.run("display-message -p -t " + q(d.dock.win) + " '#{window_width}'"); e == nil && len(lines) == 1 {
				winW, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
			}
			if winW == d.dock.hostW && env.msg.Width >= 18 && env.msg.Width <= 80 {
				log.Printf("dock: adopted width %d (border drag)", env.msg.Width)
				d.setWidth(ctl, env.msg.Width, false)
			} else {
				if winW > 0 {
					d.dock.hostW = winW
				}
				_, err = ctl.run(fmt.Sprintf("resize-pane -t %s -x %d", q(d.dock.pane), d.width()))
			}
		}
	case "width":
		// Border drag in the browse canvas: the TUI already repainted at
		// the new width; make it the sidebar's width everywhere.
		d.setWidth(ctl, env.msg.Width, true)
	case "split":
		// Agents-divider drag, sent on RELEASE. The TUI has already
		// repainted at the new ratio and owns it for its own lifetime;
		// the daemon only persists it and stamps it into future
		// snapshots, so the next dock is born with the same split.
		if f := env.msg.Split; f >= minSplit && f <= maxSplit {
			d.h.setSplit(f)
			saveOpt(ctl, optSplit, strconv.FormatFloat(f, 'f', 3, 64))
		}
	case "create":
		// `n` from the sidebar: a new session, starting in the working
		// directory of the row it was pressed on.
		err = d.createSession(ctl, env.msg.Sess, env.msg.Name)
	case "rename":
		// Inline rename from the sidebar (`r` on a session row). The
		// %session-renamed notification re-lists and the new name flows
		// back as a normal diff.
		if env.msg.Sess == "" || env.msg.Name == "" {
			err = errors.New("rename needs a session and a name")
		} else {
			_, err = ctl.run("rename-session -t " + q(env.msg.Sess) + " " + q(env.msg.Name))
		}
	case "kill":
		// `x` from the sidebar, past its y/n confirm: a session row closes the
		// whole session, an agent row closes just that agent's pane.
		err = d.killTarget(ctl, env.msg.Sess, env.msg.Pane)
	case "focus":
		// C-l from the docked idle sidebar: select the pane geometrically
		// right of it — vim-tmux-navigator semantics, no origin reset.
		if d.dock != nil && !d.dock.scrubbing {
			_, err = ctl.run("select-pane -R -t " + q(d.dock.pane))
		}
	case "goto":
		// A clicked notification. Unlike every other command here this one
		// arrives from OUTSIDE tmux — the notifier relaunched by macOS — so
		// it cannot assume a dock, a scrub, or even that the pane is in the
		// session any client is currently on.
		err = d.gotoPane(ctl, env.msg.Pane)
	case "commit":
		if d.dock != nil {
			if d.dock.scrubbing {
				err = d.commitScrub(ctl, env.msg.Window, env.msg.Pane)
			} else {
				err = d.dockCommit(ctl, env.msg.Pane)
			}
		}
	case "close":
		if d.dock != nil {
			if d.dock.scrubbing {
				// q mid-scrub: unzoom — the origin panes reappear untouched.
				d.scrubEnd(ctl, true)
				d.pushSelect(selectMsg{Type: "select", Window: d.dock.win})
			} else {
				err = d.dockClose(ctl, true)
			}
		}
	case "hello-list":
		// A fresh TUI connected, and has already painted (it sends hello
		// after its first paint). Its snapshot carried the selection and the
		// width, so these are belt-and-braces for a TUI that connected
		// before the daemon knew either.
		if d.dockW != 0 {
			d.h.send(env.sub, marshalLine(widthMsg{Type: "width", Width: d.dockW}))
		}
		// If this TUI connected mid-scrub (browse pre-zooms into a billboard
		// before the TUI subscribes, so it missed scrubStart's one-shot
		// surface push), tell it the zoomed surface width now — otherwise it
		// would paint the billboard at its pty width, which on next-3.8 is
		// still the docked width because tmux never resized a zoomed pane's
		// pty with other clients attached.
		if d.dock != nil && d.dock.scrubbing {
			d.h.send(env.sub, marshalLine(surfaceMsg{Type: "surface", Cols: d.dock.hostW}))
		}
		// dockOpen just spawned it; replay the selection it missed (and the
		// current frame, when browse pre-zoomed into a scrub).
		//
		// Replay what the hub RECORDED, not d.dock.win. Those agree whenever
		// the selection is a window, which is every path but one: M-a docks
		// and then picks an agent's PANE, and this replay — arriving after the
		// snapshot that carried the pick correctly — overwrote it with a
		// bare window, dropping the selection onto the session row. The first
		// press of the agent switcher never landed on an agent, and because
		// the cycle position HAD advanced, the second press showed the second
		// agent: the top-attention one, the whole point of the feature, was
		// unreachable. Visible as a flicker unique to M-a — the right row was
		// painted from the snapshot, then this took it away.
		if d.dock != nil {
			// The TUI is on screen: lift the open-transition tint (dockOpen)
			// so billboard frames render default-background cells normally,
			// and take the focus the split deliberately left behind (-d) —
			// handing it over only now keeps the origin pane's focus-out
			// repaint clear of its resize repaint.
			_, _ = ctl.run("set-option -p -uq -t " + q(d.dock.pane) + " window-style ; " +
				"set-option -p -uq -t " + q(d.dock.pane) + " window-active-style")
			// The focus handoff is DELAYED to dockFocusDelay past the open:
			// a focus-out within ~50ms of the split's resize throws Claude
			// Code panes into a render storm (the open flicker; ~760
			// presented frames probed vs 5 at 50ms separation), and hello
			// alone lands 26-60ms in — astride the threshold. Only the
			// select-pane waits; everything else hello carries stays
			// immediate. A hello long after the open is a TUI reconnect
			// (daemon restart, respawn) — those must not steal the keyboard
			// at all; a dock that moves or scrubs before the timer fires is
			// skipped at fire time (daemon.go).
			if age := time.Since(d.dock.openedAt); age < 2*time.Second {
				if (d.agentDelayOff || !d.agentPane(d.dock.snap.activePane)) &&
					d.dock.win == d.dock.originWin && !d.dock.scrubbing {
					// The pane losing focus is a plain one (nvim, shell):
					// no collision to avoid, take focus right away. Same
					// moved/scrubbing guards the timer applies at fire.
					_, _ = ctl.run("select-pane -t " + q(d.dock.pane))
				} else {
					wait := dockFocusDelay - age
					if wait < time.Millisecond {
						wait = time.Millisecond
					}
					d.focusPane = d.dock.pane
					if d.focusT != nil {
						d.focusT.Stop()
					}
					d.focusT = time.NewTimer(wait)
					d.focusC = d.focusT.C
				}
			}
			selWin, selPane, selQuiet := d.h.getSelect()
			if selWin == "" {
				selWin = d.dock.win
			}
			d.h.send(env.sub, marshalLine(selectMsg{
				Type: "select", Window: selWin, Pane: selPane, Quiet: selQuiet}))
			if d.dock.scrubbing && d.pv.lastPanes != nil {
				d.h.send(env.sub, d.pv.frameBytes())
			}
			log.Printf("hello-list: replay docked select=%s pane=%q quiet=%v spawn_ms=%d",
				selWin, selPane, selQuiet, time.Since(d.dock.openedAt).Milliseconds())
		}
	case "doctor":
		// Read-only by contract: a tool reached for when something looks wrong
		// must not change what you were about to look at. Replies with the
		// report rather than an ok/err, so it returns before the shared tail.
		d.h.send(env.sub, marshalLine(replyMsg{Type: "reply", OK: true, Text: d.doctor(ctl)}))
		return
	default:
		err = fmt.Errorf("unknown cmd %q", env.msg.Cmd)
	}
	wait := start.Sub(env.recv)
	if dur := time.Since(start); dur > 25*time.Millisecond || wait > 25*time.Millisecond {
		// wait = time queued behind whatever the event loop was doing
		// (a re-list, a stream tick, an earlier command) before this ran.
		log.Printf("%s took %s (queued %s)", env.msg.Cmd, dur, wait)
	} else if bench {
		log.Printf("bench cmd=%s prefetch=%v dur_us=%d", env.msg.Cmd, env.msg.Prefetch, dur.Microseconds())
	}
	r := replyMsg{Type: "reply", OK: err == nil}
	if err != nil {
		r.Err = err.Error()
	}
	d.h.send(env.sub, marshalLine(r))
}

// toggle is M-s: docked -> focus the sidebar, or close it if the keyboard is
// already there (mid-scrub: commit-and-dismiss); otherwise dock the sidebar
// into the client's current window.
func (d *daemon) toggle(ctl *control, client string) error {
	if client == "" {
		return errors.New("toggle needs a client name")
	}
	if d.dock != nil {
		if d.dock.scrubbing {
			// M-s mid-scrub commits AND dismisses — browse-era muscle
			// memory: one press lands you in the selection. (Enter is the
			// commit that keeps the sidebar docked.)
			target := d.pv.target
			sid := d.sessionOf(target)
			if sid != "" && target != d.dock.win {
				if t := d.dock.carved[target]; t != nil && t.spacer != "" {
					// Carved: swap the sidebar in first (geometry-free —
					// content in front of the user instantly), THEN undock;
					// the expand-to-full-width reflow happens behind content
					// the user is already reading, never before it.
					if err := d.dockMove(ctl, target, true, ""); err != nil {
						return err
					}
					return d.dockClose(ctl, false)
				}
				// Uncarved (huge-scrollback windows deliberately stay so):
				// the target is already at full width — land on it directly,
				// zero geometry changes, zero reflows. Committing first
				// would carve it (~200ms history reflow) only to expand it
				// right back.
				d.dock.originSess, d.dock.originWin = sid, target
				return d.dockClose(ctl, true)
			}
			return d.dockClose(ctl, false)
		}
		// Docked and idle: focus the sidebar, or close it if the keyboard is
		// already there. JetBrains tool-window semantics (alt+1 focuses,
		// then hides) — the shape this problem converges to, because one
		// state (content pane, sidebar open) has two wanted outcomes and a
		// key produces one. Closing from a content pane is the two-press
		// case; it is the rarer one, since you close the sidebar while
		// looking at it.
		//
		// A direct jump, not select-pane -L: C-h walks one pane at a time,
		// which is several presses from the far edge of a split. Window
		// matched because select-pane cannot move a client to another
		// window — off-window it would report success and change nothing,
		// so closing is the honest answer there.
		if fp, fw := d.focusOf(ctl, client); fw == d.dock.win && fp != d.dock.pane {
			_, err := ctl.run("select-pane -t " + q(d.dock.pane))
			return err
		}
		return d.dockClose(ctl, false)
	}
	return d.dockOpen(ctl, client)
}

// browseOpen is `winch browse`: dock into the client's current window and
// zoom straight into billboard scrubbing — the old full-screen browser is
// now just the dock's scrub view, opened from row one.
func (d *daemon) browseOpen(ctl *control, client string) error {
	if d.dock == nil {
		if err := d.dockOpen(ctl, client); err != nil {
			return err
		}
	}
	if d.dock.scrubbing {
		return nil
	}
	return d.scrubStart(ctl, d.dock.win)
}

// createSession makes a session and switches to it. from is the session the
// new one inherits a working directory from — you pressed `n` on that row,
// and "like this one, but new" is what that means.
//
// The switch is an ordinary unrouted one: the daemon's follow already moves
// the sidebar with it, so there is nothing session-creation-specific about
// arriving. A duplicate name is the one failure a person will actually hit,
// so it is reported where they are looking rather than only in the log.
func (d *daemon) createSession(ctl *control, from, name string) error {
	if name == "" {
		return errors.New("create needs a name")
	}
	cmd := "new-session -d -s " + q(name)
	if from != "" {
		if lines, err := ctl.run("display-message -p -t " + q(from) + " '#{pane_current_path}'"); err == nil && len(lines) == 1 {
			if cwd := strings.TrimSpace(lines[0]); cwd != "" {
				cmd += " -c " + q(cwd)
			}
		}
	}
	if _, err := ctl.run(cmd); err != nil {
		if d.dock != nil {
			_, _ = ctl.run("display-message -t " + q(d.dock.client) + " " +
				q("winch: cannot create "+name+" (name taken?)"))
		}
		return err
	}
	if d.dock == nil {
		return nil
	}
	_, err := ctl.run("switch-client -c " + q(d.dock.client) + " -t " + q(name))
	return err
}

// gotoPane takes every attached client to a pane: its session, its window,
// the pane itself. This is what clicking a desktop notification runs.
//
// All three steps, in that order, because the pane may be anywhere: a
// different window of the session you are on, or a session no client has
// looked at in an hour. select-pane alone would silently do nothing, which
// is the worst outcome for a click — you asked to be taken somewhere and the
// screen did not move.
//
// Control clients are skipped. winch's own connection is one, and switching
// it does nothing except confuse the daemon's idea of where the user is.
func (d *daemon) gotoPane(ctl *control, pane string) error {
	if pane == "" {
		return errors.New("focus needs a pane")
	}
	w, err := fetchWorld(ctl)
	if err != nil {
		return err
	}
	var win, sess string
	for _, p := range w.Panes {
		if p.ID == pane {
			win, sess = p.WindowID, p.SessionID
			break
		}
	}
	if win == "" {
		// The agent's pane died between the notification and the click.
		// Not an error worth surfacing: the notification was true when sent.
		log.Printf("focus %s: pane is gone", pane)
		return nil
	}
	cmds := []string{}
	for _, c := range w.Clients {
		if c.Name == "" || c.SessionID == sess {
			continue
		}
		cmds = append(cmds, "switch-client -c "+q(c.Name)+" -t "+q(sess))
	}
	cmds = append(cmds,
		"select-window -t "+q(win),
		"select-pane -t "+q(pane))
	_, err = ctl.runSeq(cmds...)
	return err
}

// killTarget closes a session (x on a session row) or one agent (x on an agent
// row), having first carried anything that lives there somewhere safe.
//
// An agent is a PANE, not a window. Agents share a window often enough —
// several claudes split across one workspace — and killing the window took
// every agent in it, which is not what a row that names one agent can mean.
// Killing just the pane leaves the neighbours running; when the pane was the
// only real thing in there, the window closes on its own anyway, because a
// window whose last non-spacer pane goes is reaped exactly as tmux would.
//
// The evacuation is the rest of the job. tmux's default detach-on-destroy
// throws a client out to a bare shell when its session is destroyed, and the
// sidebar pane would go down with a window it is docked in — so pressing x on
// the session you are sitting in could cost you both your sidebar and your
// terminal. winch is the one pulling the trigger, so it does not get to leave
// that to the user's tmux settings: move first, then kill.
func (d *daemon) killTarget(ctl *control, sess, pane string) error {
	switch {
	case sess == "" && pane == "":
		return errors.New("kill needs a session or a pane")
	case sess != "" && pane != "":
		return errors.New("kill takes a session or a pane, not both")
	}
	w, err := fetchWorld(ctl)
	if err != nil {
		return err
	}
	if sess != "" && !sessionExists(w, sess) {
		return nil // already gone; the diff that says so is on its way
	}
	// Killing a pane only takes its window down when nothing real is left
	// behind it — that window then joins the blast radius, since everything
	// below evacuates by window.
	paneWin, takesWindow := "", false
	if pane != "" {
		paneWin, takesWindow = d.lastRealPane(w, pane)
		if paneWin == "" {
			return nil // the pane has already gone
		}
	}
	// The blast radius: every window about to stop existing.
	doomed := func(wid string) bool {
		if wid == "" {
			return false
		}
		for _, x := range w.Windows {
			if x.ID == wid {
				return (sess != "" && x.SessionID == sess) ||
					(takesWindow && x.ID == paneWin)
			}
		}
		return false
	}

	// Everything winch is DOING to the blast radius has to stop first.
	// Navigating onto a row starts a billboard scrub of its window, which
	// carves a spacer there and swaps the sidebar in — all aimed at a window
	// that is about to stop existing. Left armed, the scrub that raced this
	// kill went on trying to swap with a pane tmux had already destroyed, and
	// the next toggle failed with "can't find window".
	if d.dock != nil {
		if d.dock.scrubbing && (doomed(d.dock.win) || doomed(d.dock.scrubWin) || doomed(d.pv.target)) {
			d.scrubEnd(ctl, true)
		}
		if doomed(d.pv.target) {
			d.pv.target = ""
			d.pv.reset()
		}
		for wid := range d.dock.carved {
			// Only a session kill removes windows itself; the spacers in them
			// die with them, so the carve describes nothing and is FORGOTTEN
			// rather than restored — a select-layout aimed at a dead window
			// errors, and tmux aborts the batch it rides in at the first one.
			//
			// A pane kill must keep its carve even when the window is on its
			// way out, because the spacer is what is left holding that window
			// open, and reapEmptyCarves closes it by walking exactly this map.
			// Forgetting it here stranded the window with a spacer in it and
			// nothing that knew to reap it.
			if sess != "" && doomed(wid) {
				delete(d.dock.carved, wid)
				d.opts.forget(scopeWindow, wid)
			}
		}
	}

	// A refuge to move to, or "" when the whole server is going. Preferring
	// the client's own session keeps the move as small as possible: closing
	// some other session should not also relocate you.
	refuge := pickRefuge(w, d.clientSession(w), doomed)

	// The sidebar first, so it survives to show the result. dockMove carries
	// the client with it, which covers the client too whenever both are in
	// the blast radius — the common case, since x is pressed on the row you
	// are looking at.
	moved := false
	if d.dock != nil && doomed(d.dock.win) {
		if refuge == "" {
			// Nowhere to go: undock cleanly rather than have the pane die
			// under us and the layout restore land on a dead window.
			_ = d.dockClose(ctl, false)
		} else if err := d.dockMove(ctl, refuge, true, ""); err != nil {
			log.Printf("kill: move sidebar to %s: %v", refuge, err)
		} else {
			d.pushSelect(selectMsg{Type: "select", Window: refuge, Quiet: true})
			moved = true
		}
	}
	// A client in the blast radius that the sidebar move did not already
	// carry — undocked, or docked in a window that is not dying.
	if refuge != "" && !moved {
		for _, c := range w.Clients {
			if !clientDoomed(w, c, doomed) {
				continue
			}
			if _, err := ctl.runSeq(
				"select-window -t "+q(refuge),
				"switch-client -c "+q(c.Name)+" -t "+q(refuge),
			); err != nil {
				log.Printf("kill: move client %s: %v", c.Name, err)
			}
		}
	}

	if pane != "" {
		log.Printf("kill pane %s (window %s dies: %v, refuge %q)", pane, paneWin, takesWindow, refuge)
		_, err = ctl.run("kill-pane -t " + q(pane))
	} else {
		log.Printf("kill session %s (refuge %q)", sess, refuge)
		_, err = ctl.run("kill-session -t " + q(sess))
	}
	return err
}

// lastRealPane returns a pane's window, and whether killing that pane leaves
// nothing in it but winch's own spacer — in which case the window goes too.
//
// The spacer has to be discounted by IDENTITY, from the carve that made it.
// pane_current_command reports `sleep` for a spacer, and a user is perfectly
// entitled to run sleep in a pane of their own.
func (d *daemon) lastRealPane(w world, pid string) (string, bool) {
	wid := ""
	for _, p := range w.Panes {
		if p.ID == pid {
			wid = p.WindowID
			break
		}
	}
	if wid == "" {
		return "", false
	}
	spacer := ""
	if d.dock != nil {
		if t := d.dock.carved[wid]; t != nil {
			spacer = t.spacer
		}
	}
	for _, p := range w.Panes {
		if p.WindowID == wid && p.ID != pid && p.ID != spacer {
			return wid, false
		}
	}
	return wid, true
}

func sessionExists(w world, sid string) bool {
	for _, s := range w.Sessions {
		if s.ID == sid {
			return true
		}
	}
	return false
}

// clientDoomed reports whether a client is looking at a window that is about
// to die. A client in a doomed session is doomed wherever it is standing —
// killing the session takes every window with it.
func clientDoomed(w world, c tclient, doomed func(string) bool) bool {
	for _, x := range w.Windows {
		if x.SessionID == c.SessionID && x.Active {
			return doomed(x.ID)
		}
	}
	return false
}

// clientSession is the session the docked client is in, "" when undocked.
func (d *daemon) clientSession(w world) string {
	if d.dock == nil {
		return ""
	}
	for _, c := range w.Clients {
		if c.Name == d.dock.client {
			return c.SessionID
		}
	}
	return ""
}

// pickRefuge chooses a surviving window to move to: one in prefer if that
// session has any left, otherwise the lowest-indexed window of the
// first-sorted surviving session. Deterministic on purpose — a rig cannot
// pin behaviour that depends on tmux's attach ordering.
func pickRefuge(w world, prefer string, doomed func(string) bool) string {
	// fetchWorld sorts windows by session then index, so the first survivor
	// seen in each scan is that session's lowest-indexed one.
	first, inPrefer := "", ""
	for _, x := range w.Windows {
		if doomed(x.ID) {
			continue
		}
		if first == "" {
			first = x.ID
		}
		if x.SessionID == prefer && inPrefer == "" {
			inPrefer = x.ID
		}
	}
	if inPrefer != "" {
		return inPrefer
	}
	return first
}

// paneNum is the numeric part of a tmux pane id ("%1572" -> 1572), for
// ordering. Unparseable ids sort last rather than colliding on 0.
func paneNum(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "%"))
	if err != nil {
		return math.MaxInt
	}
	return n
}

// focusOf is the pane the client is looking at, and its window. Asked of
// tmux rather than inferred from the world: the world knows each window's
// active pane, but which window a given CLIENT is on is exactly the thing
// that goes stale between re-lists.
func (d *daemon) focusOf(ctl *control, client string) (pane, win string) {
	lines, err := ctl.run("display-message -p -t " + q(client) + " '#{pane_id} #{window_id}'")
	if err != nil || len(lines) != 1 {
		return "", ""
	}
	f := strings.Fields(strings.TrimSpace(lines[0]))
	if len(f) != 2 {
		return "", ""
	}
	return f[0], f[1]
}

// agentAt finds an agent by pane id, or — with pane empty — the first agent
// in a window. Returns -1 for no match. agents is already sorted, so the
// window lookup yields the highest-attention agent sharing that window.
func agentAt(agents []pane, pane, win string) int {
	for i, p := range agents {
		if pane != "" && p.ID == pane {
			return i
		}
		if pane == "" && win != "" && p.WindowID == win {
			return i
		}
	}
	return -1
}

// agentsOpen (M-a): the agent switcher. M-s with the selection pinned to an
// agent row — the agent the user is already in, or failing that the
// highest-attention one. Once docked it IS M-s. With no agents it says so
// instead of docking.
//
// It used to cycle instead of closing, and used to open through browseOpen.
// Both were wrong, and interacted:
//
//   - cycling made the pick depend on how recently you last pressed the key,
//     so the same keystroke in the same place gave a different agent for ten
//     seconds afterwards. Every "it opened something random" in the logs was
//     a cycle continuation, never a bad anchor.
//   - browseOpen forced a scrub, which ZOOMS the sidebar from its 55 columns
//     to the full window and captures a billboard. M-s pays neither unless
//     you scroll off the current window. That was the whole speed gap.
//
// Now it docks and selects, nothing more. When the pinned agent is in the
// window you are already in — the usual case, since the anchor prefers the
// agent you are in — nothing scrubs at all. When it is elsewhere, the TUI
// asks for that window's frame itself and the scrub starts from there, the
// same way it would if you had scrolled onto the row by hand.
func (d *daemon) agentsOpen(ctl *control, client string) error {
	// Where the keyboard is, read BEFORE anything moves it. This is both the
	// toggle test and the anchor: the agent you are sitting in is the one the
	// list should land on.
	fp, fw := d.focusOf(ctl, client)

	if d.dock != nil {
		// Mid-scrub, or with the keyboard already in the sidebar, M-a means
		// exactly what M-s means — there is no agent to anchor on when you
		// are looking at the list, and this press is the closing one. (Off
		// the dock's window select-pane cannot reach anyway, so closing is
		// the honest answer there too.) A dock the switcher itself JUST
		// opened counts as keyboard-in-sidebar even before the delayed
		// focus handoff (tui.go's held-back hello) has landed — without
		// that, a quick M-a M-a re-anchored instead of closing.
		if d.dock.scrubbing || fw != d.dock.win || fp == d.dock.pane {
			return d.toggle(ctl, client)
		}
		if d.dock.openedBy == "agents" && time.Since(d.dock.openedAt) < 2*time.Second {
			// Straight to close: toggle would read the keyboard as being in
			// a content pane and focus the sidebar instead.
			return d.dockClose(ctl, false)
		}
	}
	rank := map[string]int{"blocked": 4, "done": 3, "working": 2, "idle": 1}
	var agents []pane
	for _, p := range d.h.getWorld().Panes {
		if p.Agent != "" {
			agents = append(agents, p)
		}
	}
	if len(agents) == 0 {
		if d.dock != nil {
			// Nothing to anchor on, but the sidebar is up and the keyboard is
			// not in it — focus it, the way M-s would. Refusing to move would
			// read as a dead key.
			return d.toggle(ctl, client)
		}
		_, err := ctl.run("display-message -t " + q(client) + " " + q("winch: no agents"))
		return err
	}
	sort.Slice(agents, func(i, j int) bool {
		ri, rj := rank[agents[i].AgentState], rank[agents[j].AgentState]
		if ri != rj {
			return ri > rj
		}
		// Equal attention: most recently changed first, matching the
		// sidebar and herdr's priority tie-break. Pane number still settles
		// agents that have never transitioned, so the order is total — and
		// it is compared as a NUMBER, because a string compare reads
		// %1572 < %4 < %77, a stable order nobody could predict.
		if agents[i].AgentSeq != agents[j].AgentSeq {
			return agents[i].AgentSeq > agents[j].AgentSeq
		}
		return paneNum(agents[i].ID) < paneNum(agents[j].ID)
	})
	// Start where the user already IS, falling back to the top-attention
	// agent (index 0) when the focus is not on an agent at all.
	next := 0
	if fp != "" {
		if i := agentAt(agents, fp, ""); i >= 0 {
			next = i
		} else if i := agentAt(agents, "", fw); i >= 0 {
			next = i
		}
	}
	pick := agents[next]
	// Anchoring is the whole point of the key, so it happens whether the
	// sidebar is being opened or merely focused. Handing an already-docked
	// sidebar to M-s instead left the selection wherever it had been parked —
	// press M-a from an agent with a session row selected and the session row
	// is what you got, which is the one thing M-a is supposed to never do.
	if d.dock == nil {
		if err := d.dockOpen(ctl, client); err != nil {
			return err
		}
		d.dock.openedBy = "agents"
	} else if _, err := ctl.run("select-pane -t " + q(d.dock.pane)); err != nil {
		return err
	}
	// Quiet: opening the sidebar is not navigation. The row is selected and
	// the list scrolls to it; Enter is what goes there.
	d.pushSelect(selectMsg{Type: "select", Window: pick.WindowID, Pane: pick.ID, Quiet: true})
	return nil
}
