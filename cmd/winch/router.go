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
	case "rename":
		// Inline rename from the sidebar (`r` on a session row). The
		// %session-renamed notification re-lists and the new name flows
		// back as a normal diff.
		if env.msg.Sess == "" || env.msg.Name == "" {
			err = errors.New("rename needs a session and a name")
		} else {
			_, err = ctl.run("rename-session -t " + q(env.msg.Sess) + " " + q(env.msg.Name))
		}
	case "focus":
		// C-l from the docked idle sidebar: select the pane geometrically
		// right of it — vim-tmux-navigator semantics, no origin reset.
		if d.dock != nil && !d.dock.scrubbing {
			_, err = ctl.run("select-pane -R -t " + q(d.dock.pane))
		}
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

// toggle is M-s: docked -> undock (mid-scrub: commit-and-dismiss); otherwise
// dock the sidebar into the current window.
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
// highest-attention one — and a second press closes it, exactly like M-s.
// With no agents it says so instead of docking.
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
	if d.dock != nil {
		// Second press closes. Same call M-s makes, so the two keys dismiss
		// identically and neither moves the client.
		return d.dockClose(ctl, false)
	}
	rank := map[string]int{"blocked": 4, "done": 3, "working": 2, "idle": 1}
	var agents []pane
	for _, p := range d.h.getWorld().Panes {
		if p.Agent != "" {
			agents = append(agents, p)
		}
	}
	if len(agents) == 0 {
		_, err := ctl.run("display-message -t " + q(client) + " " + q("winch: no agents"))
		return err
	}
	sort.Slice(agents, func(i, j int) bool {
		ri, rj := rank[agents[i].AgentState], rank[agents[j].AgentState]
		if ri != rj {
			return ri > rj
		}
		// By pane NUMBER, so equal-attention agents rank oldest to newest.
		// A string compare here reads %1572 < %4 < %77, which is a stable
		// order but not one anybody could predict — with every agent idle
		// (the common case) the tie-break IS the order, and the switcher
		// looked like it picked at random.
		return paneNum(agents[i].ID) < paneNum(agents[j].ID)
	})
	// Start where the user already IS, falling back to the top-attention
	// agent (index 0) when the focus is not on an agent at all.
	next := 0
	if fp, fw := d.focusOf(ctl, client); fp != "" {
		if i := agentAt(agents, fp, ""); i >= 0 {
			next = i
		} else if i := agentAt(agents, "", fw); i >= 0 {
			next = i
		}
	}
	pick := agents[next]
	if err := d.dockOpen(ctl, client); err != nil {
		return err
	}
	// Quiet: opening the sidebar is not navigation. The row is selected and
	// the list scrolls to it; Enter is what goes there.
	d.pushSelect(selectMsg{Type: "select", Window: pick.WindowID, Pane: pick.ID, Quiet: true})
	return nil
}
