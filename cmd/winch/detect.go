package main

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Agent state detection: which panes run coding agents, and whether each is
// working, blocked, idle, or done (finished while you weren't looking).
// Rules live in per-agent TOML manifests (manifest.go); this file owns the
// polling loop, the anti-flap policy, and publishing.
//
// Design rules from herdr's arc (docs/herdr-triage.md):
//
//   - The SCREEN is the level-triggered authority. herdr shipped Claude
//     hooks as state authority and REVERTED (v0.6.7): hooks are
//     edge-triggered and incomplete — nothing fires on interrupt or prompt
//     dismissal, and late SubagentStop events revive dead states.
//   - The OSC title is the cheap fast path: a title rule that outranks
//     every screen rule (claude's spinner) classifies without a capture.
//   - Idle is CONFIRMED, never assumed: working -> idle holds for
//     idleConfirms samples. Blocked and working publish instantly. Only
//     idle panes with an unmoved activity stamp skip the rescan.
//   - "Done" is UI state, not detection: a completion transition
//     ((working|blocked) -> idle) in a window no attached client is
//     looking at becomes "done" and sticks until the user visits it.
//
// This is a poll — the daemon's only one — because there is no event
// source: probe-verified that tmux emits NO control-mode notification for
// pane title changes, and a cross-session `split-window -d` emits nothing
// at all. The tick's own list-panes doubles as the self-heal for panes the
// notification matrix misses.

// idleConfirms is a COUNT, not a duration, and stays fixed: it is the
// anti-flap rule itself. Everything around it is wall clock, and wall clock
// is the entire cost of the rig suite — a run spent 160s of its 270s asleep,
// most of it waiting for exactly these constants to elapse.
const idleConfirms = 3

// Detection cadences. testFast (rig harness only) compresses every one of
// them; it never changes behaviour, only how long the same behaviour takes.
// Live values are in the else branch.
var detectTick, detectFastTick, idleCap, detectIdleTick, startupGrace = func() (
	tick, fast, cap_, idle, grace time.Duration) {
	if testFast {
		// Chosen against the suite running ~30-wide: 50ms ticks turned out
		// to cost more in capture-pane churn under that load than they saved
		// in latency, which showed up as flakes rather than as time.
		return 80 * time.Millisecond, 40 * time.Millisecond,
			200 * time.Millisecond, 150 * time.Millisecond, 200 * time.Millisecond
	}
	return 300 * time.Millisecond, // full cadence with agents present
		100 * time.Millisecond, // while a pending-idle hold is confirming
		700 * time.Millisecond, // the hold's wall-clock escape hatch
		2 * time.Second, // discovery only, nothing to watch
		3 * time.Second // a just-spawned agent is not judged yet
}()

// motionCap is the backstop on the still-screen gate in applyAgentState: how
// long idle verdicts may keep arriving over a MOVING screen before we stop
// disbelieving them.
//
// It should never fire. It exists because the gate's failure mode is an agent
// stuck working forever, which costs you the notification you were waiting
// for, and there is no way to be certain no pane animates something under an
// idle verdict indefinitely. Generous on purpose: a streaming message is
// seconds, so this cannot manufacture the false completion it exists to
// prevent, and it logs when it trips so the case stops being hypothetical.
var motionCap = func() time.Duration {
	if testFast {
		return 2 * time.Second
	}
	return 30 * time.Second
}()

type agentInfo struct {
	kind         string // manifest id: claude | codex | grok | ...
	state        string // "" (unknown) | working | blocked | idle | done
	reason       string // blocked only: the matched rule's label ("permission prompt")
	grace        time.Time
	pendingIdle  int       // consecutive idle samples held back
	pendingAt    time.Time // when the hold started
	title        string    // conversation name (title minus ornament), for the card
	seq          int64     // monotonic stamp of the last state change (ordering)
	lastActivity int64     // window_activity at the last screen scan
	win          string    // pane's window (for done/notify bookkeeping)
	lastGrid     uint64    // hash of the last scanned screen; see applyAgentState
}

type detectState struct {
	agents    map[string]*agentInfo // by pane id
	manifests map[string]*cManifest // normalized command -> manifest
	wrapKind  map[string]string     // pane_pid -> resolved kind for node/bun wrappers
	ticker    *time.Ticker
	tickC     <-chan time.Time
	period    time.Duration
	lastOpt   string // last @winch_agents value pushed
	seq       int64  // monotonic state-change counter; see applyAgentState
	// Notifications armed but not yet earned, by pane id, plus the config
	// they were armed under. See notifyArm.
	pending map[string]pendingNote
	ncfg    notifyCfg
}

// pendingNote is a state change waiting out the flap guard.
type pendingNote struct {
	state string    // the state that armed it; a change cancels
	at    time.Time // when it did
}

// wrapperCmds run agents under an interpreter: pane_current_command says
// "node", and only the process tree knows it's claude.
var wrapperCmds = map[string]bool{"node": true, "bun": true, "deno": true}

// agentKind normalizes a pane_current_command to a manifest id. Nix
// wrappers run as ".claude-wrapped" — strip the dressing before matching.
func (d *daemon) agentKind(cmd string) string {
	c := strings.TrimSuffix(strings.TrimPrefix(cmd, "."), "-wrapped")
	if m := d.det.manifests[c]; m != nil {
		return m.id
	}
	return ""
}

// armDetect ensures the detection ticker exists. It ALWAYS runs — a lazy
// discovery cadence with no agents known — because its own list-panes is
// what notices agent panes the notification matrix misses entirely.
func (d *daemon) armDetect(w world) {
	if d.det.ticker == nil {
		if d.det.agents == nil {
			d.det.agents = map[string]*agentInfo{}
			d.det.wrapKind = map[string]string{}
		}
		if d.det.manifests == nil {
			d.det.manifests = loadManifests()
		}
		d.det.period = detectIdleTick
		d.det.ticker = time.NewTicker(d.det.period)
		d.det.tickC = d.det.ticker.C
	}
	d.retune()
}

// retune matches the tick cadence to the moment: pending-idle holds confirm
// fast, live agents poll normal, an agentless server only pays discovery.
func (d *daemon) retune() {
	want := detectIdleTick
	for _, a := range d.det.agents {
		if a.pendingIdle > 0 {
			want = detectFastTick
			break
		}
		want = detectTick
	}
	if want != d.det.period && d.det.ticker != nil {
		d.det.period = want
		d.det.ticker.Reset(want)
	}
}

// injectAgents copies detection state onto a freshly fetched world, so world
// diffs carry agent fields without detection owning the re-list.
func (d *daemon) injectAgents(w *world) {
	for i := range w.Panes {
		if a := d.det.agents[w.Panes[i].ID]; a != nil {
			w.Panes[i].Agent = a.kind
			w.Panes[i].AgentState = a.state
			w.Panes[i].AgentReason = a.reason
			w.Panes[i].AgentSeq = a.seq
			// Detection's title wins over the world's: it is read on a
			// tick, while the world's copy is only as fresh as the last
			// re-list — which happens on tmux notifications, and a pane
			// title change emits none. The card is keyed on this now, so a
			// stale one is the wrong NAME on screen rather than a slightly
			// old tail nobody was reading.
			if a.title != "" {
				w.Panes[i].Title = a.title
			}
		}
	}
}

// visibleWindows: the windows some attached client is currently looking at.
func visibleWindows(w *world) map[string]bool {
	cur := map[string]string{} // session -> active window
	for _, win := range w.Windows {
		if win.Active {
			cur[win.SessionID] = win.ID
		}
	}
	vis := map[string]bool{}
	for _, c := range w.Clients {
		if wid := cur[c.SessionID]; wid != "" {
			vis[wid] = true
		}
	}
	return vis
}

// checkSeen clears "done" on panes whose window the user has now visited.
// Called on every re-list — client window moves always trigger one.
func (d *daemon) checkSeen(w *world) {
	if len(d.det.agents) == 0 {
		return
	}
	vis := visibleWindows(w)
	for id, a := range d.det.agents {
		if a.state == "done" && vis[a.win] {
			a.state = "idle"
			d.det.seq++
			a.seq = d.det.seq // a transition is a transition, however it happened
			log.Printf("agent %s pane=%s state=done->idle (seen)", a.kind, id)
		}
	}
}

// detectTickRun is one detection pass: classify every agent pane (title
// tier free; screen tier per the skip rule), apply anti-flap, publish a
// world diff, notify on blocked, refresh the statusline option.
func (d *daemon) detectTickRun(ctl *control, w *world) {
	lines, err := ctl.run("list-panes -a -F " +
		f("#{pane_id}", "#{pane_current_command}", "#{pane_title}", "#{window_activity}", "#{window_id}", "#{pane_pid}"))
	if err != nil {
		return
	}
	now := time.Now()
	vis := visibleWindows(w)
	seen := map[string]bool{}
	type scanReq struct {
		id    string
		a     *agentInfo
		title string
		act   int64
	}
	var scans []scanReq
	var blockedNew []string // pane ids that just turned blocked
	var doneNew []string    // pane ids that just finished a turn
	changed := false
	// soft: the card CHANGED but attention did not — the conversation was
	// renamed. Published, because the sidebar shows the name, but on a
	// lighter path than a state change: the statusline write and the blocked
	// notifications have no business firing for a rename.
	soft := false
	apply := func(id string, a *agentInfo, want string, visible bool, label string, moved bool) {
		prev := a.state
		if d.applyAgentState(id, a, want, visible, vis, moved) {
			changed = true
			if a.state == "blocked" && prev != "blocked" {
				blockedNew = append(blockedNew, id)
			}
			// "done" is idle-and-unvisited: the turn ended and nobody has
			// looked yet. That IS the end-of-turn event, and it is already
			// anti-flapped upstream by applyAgentState, so it needs no
			// special-casing here beyond being a transition.
			if a.state == "done" && prev != "done" {
				doneNew = append(doneNew, id)
			}
		}
		// The reason rides the state: set while blocked (even when the
		// matching rule changes without a transition), gone otherwise.
		want = a.state
		if want != "blocked" {
			label = ""
		}
		if a.reason != label {
			a.reason = label
			changed = true
		}
	}
	for _, ln := range lines {
		p := strings.Split(ln, sep)
		if len(p) != 6 {
			continue
		}
		id, cmd, title, wid, pid := p[0], p[1], p[2], p[4], p[5]
		act, _ := strconv.ParseInt(p[3], 10, 64)
		kind := d.agentKind(cmd)
		if kind == "" && wrapperCmds[cmd] {
			kind = d.wrappedKind(pid)
		}
		if kind == "" {
			if a := d.det.agents[id]; a != nil {
				delete(d.det.agents, id)
				changed = changed || a.state != ""
			}
			continue
		}
		seen[id] = true
		a := d.det.agents[id]
		if a == nil || a.kind != kind {
			// New agent in this pane: grace period so startup screens
			// (splash art, resumed scrollback) can't misclassify, and no
			// stale title from the previous process gets trusted.
			a = &agentInfo{kind: kind, grace: now.Add(startupGrace), win: wid}
			d.det.agents[id] = a
			changed = true // kind is publishable immediately, state isn't
			continue
		}
		a.win = wid
		if now.Before(a.grace) {
			continue
		}
		// The conversation name, kept fresh. Captured here rather than left to
		// fetchWorld because THIS is the loop that reads titles on a tick;
		// a full re-list only happens on tmux notifications, so anything
		// taken from there advances when some unrelated pane appears, which
		// is neither an animation nor a current name.
		//
		// The name matters as much as the frame now that it IS the card's
		// identity. As a droppable tail it could be stale for a long time
		// and nobody could tell.
		_, name := splitOrnament(title)
		if a.title != name {
			a.title = name
			soft = true
		}
		m := d.det.manifests[kind]
		if m == nil {
			continue
		}
		if v, ok := m.eval(newSnapshot(nil, title), true); ok && v.prio > m.maxScreenPrio {
			// Title verdict outranks every screen rule: conclusive, free.
			// moved=false costs nothing here: outranking every screen rule
			// takes a priority above maxScreenPrio, and no manifest's idle
			// title rule is anywhere near that, so this path cannot carry
			// the idle verdict the gate exists to hold back.
			if !v.skip {
				apply(id, a, v.state, v.visible, v.label, false)
			}
			continue
		}
		// herdr's skip-scan rule: only IDLE panes with an unmoved activity
		// stamp skip the screen; working/blocked always rescan — that is
		// what notices turn ends and dismissed prompts. "done" counts as
		// idle here (it IS idle, flagged unseen).
		quiet := (a.state == "idle" || a.state == "done") && act == a.lastActivity
		if !quiet {
			scans = append(scans, scanReq{id: id, a: a, title: title, act: act})
		}
	}
	for id, a := range d.det.agents {
		if !seen[id] {
			delete(d.det.agents, id)
			changed = changed || a.state != ""
		}
	}
	if len(scans) > 0 {
		caps := make([]string, 0, len(scans)*2)
		for _, s := range scans {
			caps = append(caps, "capture-pane -p -t "+q(s.id), "display-message -p "+q(frameMarker))
		}
		out, err := ctl.runSeq(caps...)
		if err == nil {
			grids := make([][]string, 1, len(scans))
			for _, ln := range out {
				if ln == frameMarker {
					grids = append(grids, nil)
					continue
				}
				grids[len(grids)-1] = append(grids[len(grids)-1], ln)
			}
			for i, s := range scans {
				if i >= len(grids) {
					break
				}
				s.a.lastActivity = s.act
				h := gridHash(grids[i])
				moved := h != s.a.lastGrid
				s.a.lastGrid = h
				m := d.det.manifests[s.a.kind]
				v, ok := m.eval(newSnapshot(grids[i], s.title), false)
				// The evidence behind a completion, recorded before apply
				// consumes it. A turn end is rare, it is the one verdict
				// that pings you, and diagnosing a wrong one from outside
				// the daemon means racing a screen that has already moved
				// on — which is exactly how this went the first time.
				was, confirms := s.a.state, s.a.pendingIdle
				switch {
				case !ok:
					apply(s.id, s.a, "idle", false, "", moved) // known agent, silent screen
				case v.skip:
					// viewer overlay: freeze the previous state
				default:
					apply(s.id, s.a, v.state, v.visible, v.label, moved)
				}
				if was == "working" && (s.a.state == "idle" || s.a.state == "done") {
					log.Printf("agent %s pane=%s completed: rule=%s moved=%v confirms=%d title=%q tail=%q",
						s.a.kind, s.id, orDash(v.rule), moved, confirms, s.title,
						lastNonEmpty(grids[i], 6))
				}
			}
		}
	}
	switch {
	case changed:
		d.publishAgents(ctl, w)
		d.notifyArm(ctl, blockedNew, "blocked", now)
		d.notifyArm(ctl, doneNew, "done", now)
		d.pushStatusOpt(ctl, w)
	case soft:
		// A frame moved and nothing else. publishAgents is a pane copy and
		// a diff unless a pane went missing, so this costs one small op per
		// turning agent — no tmux write, no notification.
		d.publishAgents(ctl, w)
	}
	// Outside the switch: an armed notification has to be reconsidered on
	// ticks where NOTHING changed, because "nothing changed" is exactly the
	// evidence that the state held long enough to be worth reporting.
	d.notifyDue(ctl, w, now)
	d.retune()
}

// publishAgents folds detection state into the world and diffs it out. If
// detection knows a pane the world doesn't (a cross-session split-window -d
// emits no notification at all), re-list for real — detection doubles as
// the self-heal for silently appearing panes.
func (d *daemon) publishAgents(ctl *control, w *world) {
	known := map[string]bool{}
	for i := range w.Panes {
		known[w.Panes[i].ID] = true
	}
	missing := false
	for id := range d.det.agents {
		if !known[id] {
			missing = true
			break
		}
	}
	w2 := *w
	if missing {
		if next, err := fetchWorld(ctl); err == nil {
			w2 = next
		}
	} else {
		w2.Panes = append([]pane(nil), w.Panes...)
		for i := range w2.Panes {
			w2.Panes[i].Agent, w2.Panes[i].AgentState = "", ""
		}
	}
	d.injectAgents(&w2)
	d.injectGit(&w2)
	ops := diffWorlds(*w, w2)
	*w = w2
	if len(ops) > 0 {
		d.h.setWorld(*w, ops, false, d.tmuxSock)
	}
}

// notifyArm records a state change that MIGHT deserve a notification. It
// does not send: notifyDue decides that once the state has held, so an agent
// that blocks and unblocks between two ticks never reaches a terminal.
//
// The config is read here rather than cached at startup because this is the
// rare path — a real transition, seconds apart at best — and reading it here
// is what makes `set -g @winch-notify all` take effect on the next agent
// instead of the next daemon.
func (d *daemon) notifyArm(ctl *control, ids []string, state string, now time.Time) {
	if len(ids) == 0 {
		return
	}
	d.det.ncfg = loadNotifyCfg(ctl)
	if !d.det.ncfg.on() {
		return
	}
	if state == "done" && !d.det.ncfg.done {
		return
	}
	if d.det.pending == nil {
		d.det.pending = map[string]pendingNote{}
	}
	for _, id := range ids {
		d.det.pending[id] = pendingNote{state: state, at: now}
	}
}

// notifyDue fires the armed notifications whose state has survived the guard,
// and quietly drops the ones that did not.
func (d *daemon) notifyDue(ctl *control, w *world, now time.Time) {
	if len(d.det.pending) == 0 {
		return
	}
	var due []string
	for id, p := range d.det.pending {
		cur := ""
		if a := d.det.agents[id]; a != nil {
			cur = a.state
		}
		fire, drop := notifyRipe(p, cur, now, d.det.ncfg.delay)
		if drop {
			delete(d.det.pending, id)
		}
		if fire {
			due = append(due, id)
		}
	}
	sort.Strings(due) // stable order for logs and rigs
	for _, id := range due {
		d.notifyFire(ctl, w, id, d.det.agents[id])
	}
}

// notifyFire delivers one agent's notification two ways: a tmux message to
// attached clients that are NOT looking at the pane (they can't already see
// it needs them), and a desktop notification to those same clients' terminals
// (notify.go), which is the half that reaches you when tmux is not on screen.
func (d *daemon) notifyFire(ctl *control, w *world, id string, a *agentInfo) {
	if a == nil {
		return
	}
	sessOf, winName := map[string]string{}, map[string]string{}
	sessName := map[string]string{}
	cur := map[string]string{}
	for _, win := range w.Windows {
		winName[win.ID] = win.Name
		sessOf[win.ID] = win.SessionID
		if win.Active {
			cur[win.SessionID] = win.ID
		}
	}
	for _, s := range w.Sessions {
		sessName[s.ID] = s.Name
	}
	where := sessName[sessOf[a.win]] + ":" + winName[a.win]

	title, body := a.kind+" needs you", where
	if a.state == "done" {
		title = a.kind + " finished"
	}
	if a.reason != "" {
		body += " — " + a.reason
	}
	if a.title != "" {
		body += " — " + a.title
	}

	cfg := d.det.ncfg
	var cmds []string
	sent := 0
	bundle := ""
	for _, c := range w.Clients {
		if notifySuppressed(cur[c.SessionID] == a.win, c.Focused) {
			continue
		}
		if bundle == "" {
			bundle = terminalBundleID(c.Term)
		}
		cmds = append(cmds, "display-message -c "+q(c.Name)+" "+q("winch: "+title+" in "+where))
		if c.TTY == "" || cfg.via == "system" {
			continue
		}
		// Per client, not per config: two clients can be two different
		// terminals, and a global dialect cannot be right for both.
		if err := notifyTTY(c.TTY, notifyPayload(cfg.resolveOSC(c.Term), title, body)); err != nil {
			log.Printf("notify %s: %v", c.Name, err)
			continue
		}
		sent++
	}
	// The system notifier is per MACHINE, not per client: asking the OS twice
	// because two clients are attached would show you the same thing twice.
	// Only fired when a client would have been notified at all, so an agent
	// you are already looking at stays silent by the same rule.
	osNote := false
	if cfg.via != "terminal" && len(cmds) > 0 {
		if err := notifySystem(title, body, bundle); err != nil {
			log.Printf("notify system: %v", err)
		} else {
			osNote = true
		}
	}
	if len(cmds) > 0 {
		log.Printf("notify %s pane=%s clients=%d desktop=%d system=%v", a.state, id, len(cmds), sent, osNote)
		_, _ = ctl.runSeq(cmds...)
	}
}

// pushStatusOpt maintains @winch_agents, a global option the status line
// can reference for free: "!2 ✓1 ✻3" (blocked / done / working counts;
// empty when quiet). Zero-cost render — no #() and no process spawns.
func (d *daemon) pushStatusOpt(ctl *control, w *world) {
	nb, nd, nw := 0, 0, 0
	for _, a := range d.det.agents {
		switch a.state {
		case "blocked":
			nb++
		case "done":
			nd++
		case "working":
			nw++
		}
	}
	var parts []string
	if nb > 0 {
		parts = append(parts, fmt.Sprintf("#[fg=red,bold]!%d#[default]", nb))
	}
	if nd > 0 {
		parts = append(parts, fmt.Sprintf("#[fg=green]✓%d#[default]", nd))
	}
	if nw > 0 {
		parts = append(parts, fmt.Sprintf("#[fg=yellow]✻%d#[default]", nw))
	}
	opt := strings.Join(parts, " ")
	if opt == d.det.lastOpt {
		return
	}
	d.det.lastOpt = opt
	cmds := []string{"set-option -g @winch_agents " + q(opt)}
	for _, c := range w.Clients {
		cmds = append(cmds, "refresh-client -S -t "+q(c.Name))
	}
	_, _ = ctl.runSeq(cmds...)
}

// wrappedKind resolves an interpreter pane (node/bun) to an agent via the
// process tree, cached per pane_pid. One `ps` exec per UNRESOLVED pid —
// never on the steady-state path.
func (d *daemon) wrappedKind(pid string) string {
	if k, ok := d.det.wrapKind[pid]; ok {
		return k
	}
	kind := ""
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err == nil {
		children := map[string][]string{}
		cmdOf := map[string]string{}
		for _, ln := range strings.Split(string(out), "\n") {
			fl := strings.Fields(ln)
			if len(fl) < 3 {
				continue
			}
			children[fl[1]] = append(children[fl[1]], fl[0])
			cmdOf[fl[0]] = strings.Join(fl[2:], " ")
		}
		var walk func(p string) string
		walk = func(p string) string {
			for _, c := range children[p] {
				for token := range d.det.manifests {
					cl := cmdOf[c]
					if strings.Contains(cl, "/"+token) || strings.HasPrefix(filepath.Base(strings.Fields(cl + " x")[0]), token) {
						return d.det.manifests[token].id
					}
				}
				if k := walk(c); k != "" {
					return k
				}
			}
			return ""
		}
		kind = walk(pid)
	}
	d.det.wrapKind[pid] = kind
	if kind != "" {
		log.Printf("agent %s resolved from wrapper pid=%s", kind, pid)
	}
	return kind
}

// applyAgentState runs the anti-flap policy and publishes the transition.
// Blocked and working land instantly; working -> idle must survive
// idleConfirms CONSECUTIVE samples (or idleCap of wall time) first — even
// with visible idle evidence (screens exist whose verdict alternates per
// scan; any bypass turns that into a flap). A completion transition
// ((working|blocked) -> idle) in a window nobody is looking at publishes
// as "done" and sticks until the user visits the window.
func (d *daemon) applyAgentState(id string, a *agentInfo, want string, visible bool, vis map[string]bool, moved bool) bool {
	if want == "idle" && a.state == "done" {
		return false // done IS idle, flagged unseen; keep the flag
	}
	if want == "idle" && a.state == "working" {
		// Keyed on pendingAt itself, not on the counter: the motion gate
		// below zeroes the counter on every moving tick, so counting would
		// restart this clock forever and motionCap could never elapse.
		if a.pendingAt.IsZero() {
			a.pendingAt = time.Now()
		}
		a.pendingIdle++
		// A still screen is part of the evidence, not just a way to save a
		// capture. While claude streams a long message the spinner line is
		// gone (the text is using that row) and the footer shows the
		// permissions-mode hint rather than "esc to interrupt", so nothing
		// matches live_turn_working and the bare prompt box wins at 950 —
		// idle, mid-turn. That frame is genuinely indistinguishable from a
		// finished turn; what separates them is that one of them is still
		// moving. So idle only accrues over ticks where the screen held
		// still, and motion resets the count.
		//
		// pendingAt is deliberately NOT reset with it: it dates the first
		// idle verdict, which is what motionCap measures from.
		if moved {
			a.pendingIdle = 0
		}
		if time.Since(a.pendingAt) >= motionCap {
			log.Printf("agent %s pane=%s idle over a moving screen for %s; believing it",
				a.kind, id, motionCap)
		} else if a.pendingIdle < idleConfirms && (moved || time.Since(a.pendingAt) < idleCap) {
			return false
		}
	}
	a.pendingIdle, a.pendingAt = 0, time.Time{}
	if want == "idle" && (a.state == "working" || a.state == "blocked") && !vis[a.win] {
		want = "done"
	}
	if a.state == want {
		return false
	}
	log.Printf("agent %s pane=%s state=%s->%s", a.kind, id, orDash(a.state), want)
	a.state = want
	// Stamp WHEN, monotonically. Equal-attention agents order by most
	// recently changed — herdr's priority tie-break — because among five
	// idle agents the one that just finished is the one you are looking
	// for, and pane number answers a question nobody asked.
	d.det.seq++
	a.seq = d.det.seq
	return true
}

// gridHash fingerprints a captured screen so the next tick can tell whether
// anything moved. FNV-1a over the rows with a separator, so that shifting a
// line break cannot collide with the same bytes laid out differently.
func gridHash(rows []string) uint64 {
	const off, prime = uint64(14695981039346656037), uint64(1099511628211)
	h := off
	for _, r := range rows {
		for i := 0; i < len(r); i++ {
			h = (h ^ uint64(r[i])) * prime
		}
		h = (h ^ '\n') * prime
	}
	return h
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// --- region helpers shared with manifest.go ---

func lastNonEmpty(lines []string, n int) []string {
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append(out, lines[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// isHRule: a line that IS a horizontal rule — leading ─ run of >=3
// (suffix text allowed). Claude's input box borders are exactly this
// (live-verified 2026-08-21).
func isHRule(s string) bool {
	t := strings.TrimSpace(s)
	n := 0
	for _, r := range t {
		if r != '─' {
			break
		}
		n++
	}
	return n >= 3
}

func afterLastHRule(lines []string) []string {
	for i := len(lines) - 1; i >= 0; i-- {
		if isHRule(lines[i]) {
			return lines[i+1:]
		}
	}
	return nil
}

// promptBoxBody: the text between the 2nd-from-last horizontal rule and the
// rule after it — claude's ─-bordered input box.
func promptBoxBody(lines []string) []string {
	last, second := -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isHRule(lines[i]) {
			if last == -1 {
				last = i
			} else {
				second = i
				break
			}
		}
	}
	if second == -1 {
		return nil
	}
	return lines[second+1 : last]
}
