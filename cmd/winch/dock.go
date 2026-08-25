package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Docked mode: the herdr layout on tmux. The TUI spawns as a real 40-col
// pane at the left edge of the client's current window; the main area is the
// user's actual panes — live, focusable, typable. Scrubbing the list shows
// other windows as billboards (preview.go frames painted by the TUI); a
// commit — Enter, or any mouse gesture on a billboard split — switches to
// the real window. M-s toggles the dock; q undocks back to the origin.
//
// The TUI pane is per-dock: dockOpen splits it into the window, dockClose
// kills it (its process dies with it, and it also exits itself when the
// daemon connection closes). There is no parked holding session — nothing
// exists between docks. `winch browse` is this mode too: dock + immediate
// zoom into scrubbing (router.go browseOpen).
//
// Rig-verified (tmux 3.7b, rigs/):
//   - `split-window -hb -f -l 40` lands at the window's left edge no matter
//     which pane is active, and the new pane takes focus (no -d)
//   - undocking hands the sidebar's 40 cols to the ADJACENT pane, not back
//     proportionally — layout restore from a saved #{window_layout} string is
//     mandatory, and restores exactly
//   - select-layout with a stale string (user split while docked) fails
//     cleanly; restores therefore run after the critical commands
//   - join-pane is gone DELIBERATELY: tmux 3.7b segfaults when a join
//     destroys its source window while resizing a tree-mode pane
//     (window_tree_build NULL deref) — the old join-based dock had exactly
//     that shape. The in-place split destroys nothing (rigs/tree_test.go).
//
// Sequencing rules that keep transitions invisible:
//   - everything the arriving window needs (swap, select-window, status pad,
//     @winch_docked) rides ONE control sequence BEFORE switch-client — the
//     status pad landing after the switch is a visible flicker
//   - undock + layout restore ride ONE sequence — as separate round trips,
//     apps in the window (nvim) see two SIGWINCH resizes and jitter
//
// While docked, tools may reshape the window deliberately (tmux-equalize-nvim
// equalizes the main region and sets @winch_layout_dirty). On leave, a dirty
// window gets a PROPORTIONAL give-back (layout.go: drop the sidebar leaf,
// rescale) instead of the pre-dock snapshot, which would undo the change.

// winSnap is the GEOMETRY needed to put a window back the way it was before
// the sidebar entered it. Queried at point-of-use, never from the cached world:
// cross-session geometry only refreshes on re-list.
//
// The window OPTIONS the dock overwrites (automatic-rename,
// pane-border-indicators) are not here — owned.go holds those, along with every
// other option winch takes from the user.
type winSnap struct {
	layout     string
	activePane string
	name       string
}

type dockState struct {
	client     string
	pane       string // the sidebar TUI pane (spawned by dockOpen, dies at undock)
	win        string // window currently hosting the sidebar
	sess       string // session of win
	originSess string // where q returns to
	originWin  string
	snap       winSnap   // pre-dock geometry of win
	openedAt   time.Time // dockOpen time; hello-list logs TUI spawn latency

	// scrubbing: the sidebar pane is ZOOMED and the main area shows live
	// billboards of the selection instead of real windows. Zoom leaves the
	// hidden panes untouched (rig-verified: sizes byte-exact, zero app
	// reflows) — scrubbing costs captures, not window mutations. The real
	// window materializes on commit (swap-from-zoomed auto-unzooms), and
	// landing back on the docked window is a free unzoom.
	scrubbing bool

	// scrubWin: the window row 0 of the status line is describing instead of
	// the session's own format. Empty means the bar says what it normally
	// says. Purely an input to desiredOpts — nothing here remembers what the
	// override displaced, because the registry does.
	scrubWin string

	// hostW: the docked window's width at the last check. Tells a border
	// drag (window width unchanged -> adopt the new pane width) apart from
	// a client resize (window width changed -> re-assert the chosen width).
	hostW int

	// carved: every window that holds the dock geometry while docked. A
	// 40-col spacer pane occupies the sidebar's slot whenever the sidebar
	// itself is elsewhere, so a window's mains NEVER resize between visits:
	// entering is a geometry-free swap-pane (sidebar and spacer trade
	// places, ~7ms even on 700k-line-history panes), and billboards are the
	// docked reality by construction — the spacer is carved by the same
	// split the dock itself would make. The alternative (resizing windows
	// to the main-area width and back) reflows every pane's entire
	// scrollback history in tmux, which stalls the server ~200ms per big
	// agent pane and flushes intermediate frames (the enter flicker).
	carved map[string]*carveState
}

// carveState is one spacer-held window's restore info. Geometry only — the
// window options frozen at carve are the registry's (owned.go), and are given
// back when the window stops being held rather than when its spacer dies.
type carveState struct {
	spacer string // spacer pane id; empty while the sidebar is in this window
	orig   string // pre-carve full-width layout, replayed at release
}

// listWidth is the sidebar's DEFAULT column width; the live width is
// d.width() — the user can retune it by dragging (the pane border when
// docked, the painted │ when browsing) and the daemon adopts it.
const listWidth = 26

// width is the sidebar's current column width.
func (d *daemon) width() int {
	if d.dockW == 0 {
		return listWidth
	}
	return d.dockW
}

// layoutWidth parses the window width out of a #{window_layout} string
// ("c5d4,200x50,0,0,..."): the cheap window-geometry source that rides
// every re-list, no extra round trip.
func layoutWidth(layout string) int {
	i := strings.Index(layout, ",")
	if i < 0 {
		return 0
	}
	rest := layout[i+1:]
	j := strings.Index(rest, "x")
	if j <= 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0
	}
	return n
}

// spacerCmd runs inside spacer panes — distinctive so a startup sweep can
// identify spacers leaked by a crashed daemon exactly.
const spacerCmd = "sleep 100000001"

// carveHistoryMax caps how much scrollback a window may carry and still get
// pre-carved during scrubbing. tmux reflows a pane's entire history
// synchronously on width change — ~250ms at 660k lines (live-measured) —
// and a carve + its later release would pay that stall TWICE per dock
// session just for billboarding past the window. Above the cap the
// billboard is a scaled approximation and the carve happens only on entry.
const carveHistoryMax = 100_000

// Releases (kill spacer + replay layout) stall the tmux server for the same
// history-reflow reason, so they never run inline with an undock: the
// transition paints first (releaseSettle), then pending windows drain one
// per tick, yielding to any queued user input.
const (
	releaseSettle = 120 * time.Millisecond
	releaseTick   = 50 * time.Millisecond
)

// releaseItem is one spacer-held window awaiting restore after undock.
type releaseItem struct {
	wid string
	t   *carveState
}

// deferReleases queues every spacer-held window for restore and arms the
// drain timer. Re-docking before the drain finishes ADOPTS the queue back
// instead (dockOpen) — the carves are still valid, so a quick M-s round
// trip costs nothing.
func (d *daemon) deferReleases(p *dockState) {
	for wid, t := range p.carved {
		delete(p.carved, wid)
		if t.spacer == "" {
			continue
		}
		d.pendingRelease = append(d.pendingRelease, releaseItem{wid: wid, t: t})
	}
	if len(d.pendingRelease) > 0 {
		d.armRelease(releaseSettle)
	}
}

func (d *daemon) armRelease(after time.Duration) {
	if d.releaseT != nil {
		d.releaseT.Stop()
	}
	d.releaseT = time.NewTimer(after)
	d.releaseC = d.releaseT.C
}

// releaseOne puts one spacer-held window back exactly as it was: kill the
// spacer and replay the original layout in one batch (the two reflows
// coalesce; letting tmux expand into the hole would drift borders ±1).
func (d *daemon) releaseOne(ctl *control, it releaseItem) {
	lay, dirty := d.leaveInfo(ctl, it.wid)
	seq := []string{"kill-pane -t " + q(it.t.spacer)}
	if rl := d.leaveLayout(it.wid, it.t.orig, lay, dirty, it.t.spacer); rl != "" {
		seq = append(seq, "select-layout -t "+q(it.wid)+" "+q(rl))
	}
	seq = append(seq, "set-option -w -uq -t "+q(it.wid)+" @winch_layout_dirty")
	if _, err := ctl.runSeq(seq...); err != nil {
		log.Printf("release %s: %v", it.wid, err)
	} else if bench {
		log.Printf("bench release win=%s", it.wid)
	}
}

// sweepLegacyState removes what daemons from BEFORE the owned-option registry
// left behind. Those builds wrote no @winch_saved_* mark, so sweepOwned cannot
// see their leftovers at all, and nothing else ever will: the bar stays shifted
// (or pinned to whatever session a scrub was pointing at) for every client that
// attaches, forever.
//
// Keyed on CONTENT, which is the best those builds allow. Both shapes are
// winch's own — a pad references @winch_win, a scrub override runs a filtered
// #{S:} loop — and nothing a theme emits looks like either, so a format the
// user wrote is never touched. Deletable once no pre-registry daemon can still
// be running against a live server; there is no hurry, it costs one round trip
// per session at attach.
func (d *daemon) sweepLegacyState(ctl *control) {
	rows, err := ctl.run("list-sessions -F " + f("#{session_id}", "#{@winch_docked}", "#{@winch_saved_status_format}"))
	if err != nil {
		return
	}
	for _, ln := range rows {
		p := strings.Split(ln, sep)
		if len(p) != 3 || p[2] != "" {
			continue // marked: sweepOwned handles it exactly
		}
		sid := p[0]
		var cmds []string
		if p[1] != "" && !d.opts.owns(optKey{scopeSession, sid, "@winch_docked"}) {
			cmds = append(cmds, "set-option -uq -t "+q(sid)+" @winch_docked")
		}
		if !d.opts.owns(optKey{scopeSession, sid, "status-format"}) {
			if cur, cerr := ctl.run("show-options -q -t " + q(sid) + " status-format"); cerr == nil {
				joined := strings.Join(cur, "\n")
				if strings.Contains(joined, padWin) || strings.Contains(joined, scrubFmtMark) {
					cmds = append(cmds,
						"set-option -uq -t "+q(sid)+" status-format",
						"set-option -uq -t "+q(sid)+" @winch_win")
				}
			}
		}
		if len(cmds) > 0 {
			_, _ = ctl.runSeq(cmds...)
			log.Printf("swept pre-registry winch state on %s", sid)
		}
	}
}

// sweepLegacyPad removes the status-left pad that winch installed before the
// pad moved into status-format. Those builds kept the original in daemon
// memory, so upgrading past them STRANDS the pad: the bar stays shifted by the
// old sidebar width for every client that ever attaches, and nothing left in
// the option tree explains why. Nothing else will ever clear it — the current
// daemon has no reason to look at status-left at all.
//
// Recognised by shape, since those builds wrote no mark: an INVISIBLE opening
// style — foreground set to the background, which is the only way to hide a
// run of columns — followed by at least a sidebar's worth of spaces. Both
// stranded generations open exactly that way, the later one going on to a
// border glyph. No theme paints bg onto fg and then holds it for 18 columns.
func (d *daemon) sweepLegacyPad(ctl *control) {
	sids, err := ctl.run("list-sessions -F " + f("#{session_id}"))
	if err != nil {
		return
	}
	for _, sid := range sids {
		// -v does not inherit, so this is the SESSION's own value or nothing
		// — a global status-left is the user's and must survive.
		lines, err := ctl.run("show-options -t " + q(sid) + " -v status-left")
		if err != nil || len(lines) != 1 || !legacyPad(lines[0]) {
			continue
		}
		_, _ = ctl.runSeq(
			"set-option -uq -t "+q(sid)+" status-left",
			"set-option -uq -t "+q(sid)+" status-left-length")
		log.Printf("swept legacy status-left pad on %s", sid)
	}
}

var legacyPadRe = regexp.MustCompile(`^#\[bg=([^],]+),fg=([^],]+)\]( +)`)

// legacyPad reports whether a status-left is one of those stranded pads.
func legacyPad(v string) bool {
	m := legacyPadRe.FindStringSubmatch(v)
	return m != nil && m[1] == m[2] && len(m[3]) >= minWidth
}

// sweepSpacers kills spacer panes a previous daemon left behind (it died or
// was killed while docked) — matched by their distinctive start command.
func (d *daemon) sweepSpacers(ctl *control) {
	lines, err := ctl.run("list-panes -a -F " + f("#{pane_id}", "#{pane_start_command}"))
	if err != nil {
		return
	}
	for _, ln := range lines {
		p := strings.Split(ln, sep)
		if len(p) != 2 {
			continue
		}
		// tmux wraps #{pane_start_command} in double quotes (3.7).
		if strings.Trim(p[1], `"`) == spacerCmd {
			_, _ = ctl.run("kill-pane -t " + q(p[0]))
			log.Printf("swept leaked spacer %s", p[0])
		}
	}
}

// The status pad (statusPadCmds) shifts the status line's content past
// the sidebar column: width cols of pane + 1 col of pane border.

func snapQuery(wid string) string {
	return "display-message -p -t " + q(wid) + " -F " +
		f("#{window_layout}", "#{pane_id}", "#{window_name}")
}

func parseSnap(line string) (winSnap, error) {
	p := strings.Split(line, sep)
	if len(p) != 3 {
		return winSnap{}, fmt.Errorf("bad snapshot %q", line)
	}
	return winSnap{layout: p[0], activePane: p[1], name: p[2]}, nil
}

// layoutDims reads the window dimensions off a #{window_layout} string's
// "checksum,WxH,..." prefix.
func layoutDims(layout string) (int, int) {
	_, rest, ok := strings.Cut(layout, ",")
	if !ok {
		return 0, 0
	}
	ws, rest, ok := strings.Cut(rest, "x")
	if !ok {
		return 0, 0
	}
	hs, _, ok := strings.Cut(rest, ",")
	if !ok {
		return 0, 0
	}
	w, _ := strconv.Atoi(ws)
	h, _ := strconv.Atoi(hs)
	return w, h
}

func (d *daemon) winSnapshot(ctl *control, wid string) (winSnap, error) {
	lines, err := ctl.run(snapQuery(wid))
	if err != nil {
		return winSnap{}, err
	}
	if len(lines) == 0 {
		return winSnap{}, fmt.Errorf("no snapshot for %s", wid)
	}
	return parseSnap(lines[0])
}

// leaveInfo queries what restoring the window being left needs: its CURRENT
// docked layout (for proportional give-back) and the dirty marker.
func (d *daemon) leaveInfo(ctl *control, wid string) (layout string, dirty bool) {
	lines, err := ctl.run("display-message -p -t " + q(wid) + " -F " +
		f("#{window_layout}", "#{@winch_layout_dirty}"))
	if err != nil || len(lines) == 0 {
		return "", false
	}
	p := strings.Split(lines[0], sep)
	if len(p) != 2 {
		return "", false
	}
	return p[0], p[1] == "1"
}

// otherClientOn reports whether any client besides the docked one is
// attached to the window's session — pre-rendering a window such a client
// might be viewing would visibly resize it under them.
func (d *daemon) otherClientOn(wid string) bool {
	w := d.h.getWorld()
	sid := ""
	for _, win := range w.Windows {
		if win.ID == wid {
			sid = win.SessionID
			break
		}
	}
	if sid == "" {
		return true // unknown window: don't touch it
	}
	for _, c := range w.Clients {
		if c.SessionID == sid && (d.dock == nil || c.Name != d.dock.client) {
			return true
		}
	}
	return false
}

// sessionOf resolves a window's session from the world model.
func (d *daemon) sessionOf(wid string) string {
	for _, w := range d.h.getWorld().Windows {
		if w.ID == wid {
			return w.SessionID
		}
	}
	return ""
}

// tuiCommand is the shell command a sidebar TUI pane runs.
func (d *daemon) tuiCommand() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := self + " -S " + d.tmuxSock + " tui"
	env := ""
	if bench {
		env += " WINCH_BENCH=1"
	}
	if testFast {
		env += " WINCH_TEST_FAST=1"
	}
	if env != "" {
		cmd = "env" + env + " " + cmd
	}
	return cmd, nil
}

// intentFor describes the options a PROSPECTIVE dock state would own. Spelled
// out rather than read off d.dock because the moves that need it build their
// batch before they mutate any state — a batch that fails must leave the dock
// exactly where it was.
//
// held is every window winch is holding geometry in: the one the sidebar is in
// plus every spacer-held one, since a spacer-held window is still wearing the
// dock's shape and still has the sidebar's border in it.
func (d *daemon) intentFor(ctl *control, sess, win string, held []string, scrubWin string) optIntent {
	if sess == "" {
		return optIntent{}
	}
	in := optIntent{
		sess:  sess,
		win:   win,
		held:  held,
		width: d.width(),
		rows:  d.opts.statusRows(ctl, sess),
	}
	if scrubWin != "" {
		if ss := d.sessionOf(scrubWin); ss != "" {
			in.scrubWin, in.scrubSess = scrubWin, ss
		}
	}
	return in
}

// dockHeld is the held-window list for the dock as it stands.
func dockHeld(p *dockState) []string {
	held := make([]string, 0, len(p.carved)+1)
	held = append(held, p.win)
	for wid := range p.carved {
		held = append(held, wid)
	}
	return held
}

// dockPlan is the difference between what winch owns and what the dock as it
// stands says it should own. For the steady-state callers — a scrub starting or
// ending, a width change — where there is no prospective state to guard.
func (d *daemon) dockPlan(ctl *control, p *dockState) (install, restore []string, commit func()) {
	if p == nil {
		return nil, nil, func() {}
	}
	in := d.intentFor(ctl, p.sess, p.win, dockHeld(p), p.scrubWin)
	return d.opts.plan(readOpts(ctl), desiredOpts(in))
}

// applyDockPlan reconciles the owned options with no batch to ride along with —
// the steady-state paths, where the only thing changing is what winch wants an
// option it already holds to say.
func (d *daemon) applyDockPlan(ctl *control, p *dockState) {
	install, restore, commit := d.dockPlan(ctl, p)
	if len(install) == 0 && len(restore) == 0 {
		return
	}
	// Separate lines in one write: a bad value in an install can abort its own
	// line, and a restore is not something to lose to it. runPipelined skips an
	// empty side rather than sending a bare newline for it.
	_, errs := ctl.runPipelined(restore, install)
	for _, err := range errs {
		if err != nil {
			log.Printf("option plan: %v", err)
			return
		}
	}
	commit()
}

// paneInWindow reports whether the world knows pid as a pane of wid.
func (d *daemon) paneInWindow(pid, wid string) bool {
	for _, pn := range d.h.getWorld().Panes {
		if pn.ID == pid {
			return pn.WindowID == wid
		}
	}
	return false
}

// dockOpen docks the sidebar into the client's current window: the TUI
// spawns as a fresh 40-col pane at the left edge. It connects to the daemon
// a beat after the split; the hello-list replay hands it the selection.
func (d *daemon) dockOpen(ctl *control, client string) error {
	if client == "" {
		return errors.New("dock needs a client name")
	}
	sid, wid, cw, ch, err := d.clientView(ctl, client)
	if err != nil {
		return err
	}
	// Adopt releases still pending from the previous dock session: their
	// carves are valid, so a quick M-s round trip re-uses them for free.
	// The window being docked INTO can't keep a spacer (the sidebar splits
	// beside it) — that one is released inline, before its snapshot.
	adopted := map[string]*carveState{}
	if len(d.pendingRelease) > 0 {
		for _, it := range d.pendingRelease {
			if it.wid == wid {
				d.releaseOne(ctl, it)
				continue
			}
			adopted[it.wid] = it.t
		}
		d.pendingRelease = nil
	}
	snap, err := d.winSnapshot(ctl, wid)
	if err != nil {
		return err
	}
	tuiCmd, err := d.tuiCommand()
	if err != nil {
		return err
	}
	p := &dockState{client: client, win: wid, sess: sid, originSess: sid, originWin: wid, snap: snap,
		carved: adopted, openedAt: time.Now(), hostW: layoutWidth(snap.layout)}
	// The owned options go first in the batch, and the rename freeze is why:
	// the sidebar takes focus when it lands, and an automatic-rename window
	// would flip its name to "winch" (the sh-era bug). The new pane lands at
	// {top-left} deterministically, so its own pane options ride here too.
	install, _, commit := d.dockPlan(ctl, p)
	seq := install
	seq = append(seq,
		fmt.Sprintf("split-window -hb -f -l %d -P -F '#{pane_id}' -t %s %s",
			d.width(), q(wid), q(tuiCmd)),
		"set-option -p -t "+q(wid+".{top-left}")+" @winch_sidebar 1",
		// Pin the sidebar's own edge so it holds one colour through focus
		// changes. Per PANE, so every other border in the window still
		// highlights normally. Pane options ride the pane through swap-pane,
		// so this is set once at spawn.
		//
		// Both styles are pinned because tmux reads BOTH from the pane owning
		// the border — screen_redraw_draw_borders_style() calls
		// style_apply(gc, wp->options, ...) for the active and the normal case
		// alike — so pinning only one leaves the other free to dim.
		"set-option -p -t "+q(wid+".{top-left}")+" pane-border-style "+q(uiSeamStyle),
		"set-option -p -t "+q(wid+".{top-left}")+" pane-active-border-style "+q(uiSeamStyle),
	)
	lines, err := ctl.runSeq(seq...)
	if err != nil {
		return err
	}
	commit()
	for _, ln := range lines {
		if strings.HasPrefix(ln, "%") {
			p.pane = ln
			break
		}
	}
	if p.pane == "" {
		return errors.New("dock split returned no pane id")
	}
	d.dock = p
	n := d.pushSelect(selectMsg{Type: "select", Window: wid})
	log.Printf("dock open client=%s win=%s/%s pane=%s size=%dx%d select_receivers=%d",
		client, sid, wid, p.pane, cw, ch, n)
	return nil
}

// While scrubbing, the status line keeps describing the ORIGIN — the client
// truly hasn't moved — while the canvas shows the target: the bar "doesn't
// change until enter". So each scrub step overrides the origin session's
// status-format[0]: a filtered #{S:} loop renders the TARGET session's
// window list (a nested #{W:} takes the looped session's context —
// probe-verified) with the scrub target marked current, and status-right
// re-expanded in that context so #S names the target session. The theme's
// own window-status formats and styles are referenced, never copied, so any
// theme renders itself.
// scrubFmtMark identifies a status-format[0] THIS daemon wrote. The filtered
// #{S:} loop is the whole trick and nothing a theme emits looks like it, so
// a sweep can recognise a leaked override without touching a session-level
// format the user set themselves. TestScrubFmtMarked pins the tie.
const scrubFmtMark = "#{S:#{?#{==:#{session_id},$"

func scrubStatusFormat(sid, wid string) string {
	curWin := "#{==:#{window_id}," + wid + "}"
	entry := "#{?" + curWin +
		",#[#{E:window-status-current-style}]#[push-default]#{T:window-status-current-format}#[pop-default]" +
		",#[#{E:window-status-style}]#[push-default]#{T:window-status-format}#[pop-default]}" +
		"#[default]#{?loop_last_flag,,#{E:window-status-separator}}"
	inSess := "#{==:#{session_id}," + sid + "}"
	return "#[align=left range=left #{E:status-left-style}]#[push-default]" +
		"#{T;=/#{status-left-length}:status-left}#[pop-default]#[norange default]" +
		"#[list=on align=#{status-justify}]#[list=left-marker]<#[list=right-marker]>#[list=on]" +
		"#{S:#{?" + inSess + ",#{W:" + entry + "},}}" +
		"#[nolist align=right range=right #{E:status-right-style}]#[push-default]" +
		"#{S:#{?" + inSess + ",#{T;=/#{status-right-length}:status-right},}}" +
		"#[pop-default]#[norange default]"
}

// scrubStatusSet points the origin's status line at the scrub target. Nothing
// is saved here: pointing row 0 somewhere else is a different DESIRED value for
// an option winch already owns, so the registry rewrites it and goes on holding
// the session's original underneath.
func (d *daemon) scrubStatusSet(ctl *control, wid string) {
	p := d.dock
	if p == nil || d.sessionOf(wid) == "" {
		return
	}
	p.scrubWin = wid
	d.applyDockPlan(ctl, p)
}

// scrubStatusCmds builds the restore: row 0 back to saying what the session
// says, still wrapped in the pad — the sidebar is still docked. Returned rather
// than run so it can ride the unzoom batch, which is one redraw.
func (d *daemon) scrubStatusCmds(ctl *control, p *dockState) []string {
	if p.scrubWin == "" {
		return nil
	}
	p.scrubWin = ""
	install, _, commit := d.dockPlan(ctl, p)
	// Nothing is being given up, so no failure here can strand a release:
	// commit alongside the commands rather than after them.
	commit()
	return install
}

// scrubStart begins billboard scrubbing: capture the target FIRST (the TUI
// caches the frame before its WINCH repaint, so the canvas fills instantly),
// then zoom the sidebar to full window. The client never leaves the docked
// window — status line, layouts, and every hidden pane stay untouched.
func (d *daemon) scrubStart(ctl *control, wid string) error {
	p := d.dock
	if p == nil || p.scrubbing {
		return nil
	}
	// Force the first frame out full even if content hasn't changed since
	// this window was last streamed: the TUI won't paint a stale cache, so
	// a deduped or delta first frame would leave the canvas empty.
	d.pv.reset()
	if err := d.preview(ctl, wid, false, false); err != nil {
		return err
	}
	if _, err := ctl.runSeq(
		"resize-pane -Z -t "+q(p.pane),
		"select-pane -t "+q(p.pane)); err != nil {
		return err
	}
	p.scrubbing = true
	d.startStream()
	d.scrubStatusSet(ctl, wid)
	log.Printf("scrub start from=%s target=%s", p.win, wid)
	return nil
}

// scrubEnd stops billboard scrubbing. unzoom=true is the landed-back-home
// path (Enter/q on the docked window): the real panes reappear exactly as
// they were. unzoom=false is for paths that dispose of the zoom some other
// way (the commit swap carries it out, an external escape already unzoomed).
func (d *daemon) scrubEnd(ctl *control, unzoom bool) {
	p := d.dock
	if p == nil || !p.scrubbing {
		return
	}
	p.scrubbing = false
	d.stopStream()
	fmtRestore := d.scrubStatusCmds(ctl, p)
	if unzoom && altScreen {
		// The TUI holds the alternate screen, so tmux CLIPS this pane's grid
		// on the 480->26 shrink instead of reflowing it. The zoomed layout
		// already paints the list in columns 1..listW, so the clip leaves
		// exactly the list: unzoom straight into a sidebar that is already
		// correct. No respawn — killing the process blanked the strip for
		// ~6ms, one presented frame in which the whole window came back
		// except the sidebar, which read as a flicker on every commit that
		// landed back on the docked window.
		seq := append(fmtRestore,
			"resize-pane -Z -t "+q(p.pane),
			fmt.Sprintf("resize-pane -t %s -x %d", q(p.pane), d.width()))
		if _, err := ctl.runSeq(seq...); err == nil {
			log.Printf("scrub end win=%s unzoom=clip", p.win)
			return
		}
		log.Printf("scrub end: clip unzoom failed, respawning")
	}
	if unzoom {
		// alternate-screen off (or the clip path errored): tmux WILL reflow
		// the canvas-filled grid into the strip. Respawn a fresh TUI into
		// the pane first — respawn clears the grid at full width — then
		// unzoom a clean grid in the same batch. The pane id is stable, so
		// no dock state changes.
		if tuiCmd, err := d.tuiCommand(); err == nil {
			seq := append(fmtRestore,
				"respawn-pane -k -t "+q(p.pane)+" "+q(tuiCmd),
				"resize-pane -Z -t "+q(p.pane),
				// unzoom lands at the SPLIT width; if the width was
				// retuned mid-scrub, assert the current one
				fmt.Sprintf("resize-pane -t %s -x %d", q(p.pane), d.width()))
			if _, err := ctl.runSeq(seq...); err == nil {
				log.Printf("scrub end win=%s unzoom=respawn", p.win)
				return
			}
			log.Printf("scrub end: respawn failed, plain unzoom")
		}
		_, _ = ctl.runSeq("resize-pane -Z -t "+q(p.pane),
			fmt.Sprintf("resize-pane -t %s -x %d", q(p.pane), d.width()))
	} else if len(fmtRestore) > 0 {
		_, _ = ctl.runSeq(fmtRestore...)
	}
	log.Printf("scrub end win=%s unzoom=%v", p.win, unzoom)
}

// pushSelect broadcasts the sidebar's selection AND records it, so a TUI
// spawned afterwards is born with it (snapshotMsg.Select) instead of
// painting a default row and correcting a beat later, which is visible.
func (d *daemon) pushSelect(m selectMsg) int {
	d.h.setSelect(m.Window, m.Pane)
	return d.h.sendRole("list", marshalLine(m))
}

// dockMove moves the main area to wid FOR REAL. On a spacer-held window this
// is a geometry-free swap-pane — the sidebar and the spacer trade places, no
// pane in either window changes size, so there is nothing to reflow, no
// intermediate frame to flush, and nothing to restore on the way out. A
// first-visit window gets its spacer slot carved (the dock's own split) and
// the sidebar swapped into it, all in one batch. Either way the OLD window
// inherits the spacer and keeps its docked geometry until release. Used by
// commit-from-scrub, routed nav, and unrouted-switch follow. Everything the
// arriving session needs (status pad, @winch_docked) is in the sequence
// BEFORE switch-client — after it, the pad lands a frame late and the status
// line visibly flickers. focusMain puts the keyboard in the window's own
// pane; false keeps focus in the sidebar.
// focusPane, when set and live in wid, is the pane to land on instead of the
// window's own active pane — a billboard click commits onto the split it hit.
func (d *daemon) dockMove(ctl *control, wid string, focusMain bool, focusPane string) error {
	p := d.dock
	if p == nil || wid == "" || wid == p.win {
		return nil
	}
	sidN := d.sessionOf(wid)
	if sidN == "" {
		return fmt.Errorf("scrub target %s unknown", wid)
	}
	// One round trip: snapshot of the window we enter, plus the CURRENT
	// active pane of the one we leave (for its focus restore).
	qlines, err := ctl.runSeq(
		snapQuery(wid),
		"display-message -p -t "+q(p.win)+" -F "+f("#{pane_id}", "#{window_width}", "#{window_height}"))
	if err != nil {
		return err
	}
	if len(qlines) < 1 {
		return errors.New("scrub query came back short")
	}
	snapN, err := parseSnap(qlines[0])
	if err != nil {
		return err
	}
	curActive, dockW, dockH := "", 0, 0
	if len(qlines) >= 2 {
		if pp := strings.Split(qlines[1], sep); len(pp) == 3 {
			curActive = pp[0]
			dockW, _ = strconv.Atoi(pp[1])
			dockH, _ = strconv.Atoi(pp[2])
		}
	}
	tgt := p.carved[wid]
	if tgt != nil {
		// Spacer-held: the live layout is the docked one, so a later release
		// must replay the pre-carve original.
		snapN.layout = tgt.orig
	}
	// The options the move WILL want, computed against the prospective state:
	// the arriving session, the arriving window, and every window winch is
	// holding after the move — which is everything it holds now plus the one
	// being entered, since the one being left keeps its spacer. Nothing is
	// recorded until the batch lands (commit, below), so a failure leaves the
	// registry believing exactly what is still true.
	held := append(dockHeld(p), wid)
	install, restore, commit := d.opts.plan(readOpts(ctl),
		desiredOpts(d.intentFor(ctl, sidN, wid, held, "")))

	// Options first in the batch: the rename freeze has to beat the swap that
	// puts the sidebar in, or an automatic-rename window renames itself to it.
	critical := append([]string(nil), install...)
	sizeStale := false
	if tgt != nil && tgt.spacer != "" {
		critical = append(critical,
			"swap-pane -d -s "+q(p.pane)+" -t "+q(tgt.spacer))
	} else {
		// First visit at entry (never billboarded, or too heavy to carve
		// during scrub): normalize a stale-sized window in the same batch —
		// splitting at stale dimensions and letting select-window resize it
		// after would shift every border the billboard promised.
		if tW, tH := layoutDims(snapN.layout); tW > 0 && dockW > 0 && (tW != dockW || tH != dockH) {
			sizeStale = true
			critical = append(critical,
				fmt.Sprintf("resize-window -x %d -y %d -t %s", dockW, dockH, q(wid)))
		}
		critical = append(critical,
			fmt.Sprintf("split-window -d -hb -f -l %d -P -F '#{pane_id}' -t %s %s",
				d.width(), q(wid), q(spacerCmd)),
			"swap-pane -d -s "+q(p.pane)+" -t "+q(wid+".{top-left}"))
	}
	critical = append(critical, "select-window -t "+q(wid))
	if sizeStale {
		// After select-window the client is on the window: latest resolves
		// to the size just set — no second resize, sizing follows the
		// client again from here.
		critical = append(critical, "set-option -w -t "+q(wid)+" window-size latest")
	}
	if sidN != p.sess {
		// Last, so everything the arriving session needs is already set when
		// the client lands on it — a pad arriving after the switch is a
		// visible flicker.
		critical = append(critical, "switch-client -c "+q(p.client)+" -t "+q(sidN))
	}
	if focusMain {
		focus := snapN.activePane
		if focusPane != "" && d.paneInWindow(focusPane, wid) {
			focus = focusPane
		}
		critical = append(critical, "select-pane -t "+q(focus))
	} else {
		// swap-pane, unlike join, does not hand the sidebar focus.
		critical = append(critical, "select-pane -t "+q(p.pane))
	}
	// The old window keeps its docked geometry (the spacer fills the
	// sidebar's slot) — the only restore is keyboard focus, so the spacer
	// isn't the active pane if the user switches back by hand. Prefer the
	// pane the user was ACTUALLY in (they may have moved since docking);
	// the dock-time snapshot is the fallback when the sidebar held focus.
	leaveFocus := p.snap.activePane
	if curActive != "" && curActive != p.pane {
		leaveFocus = curActive
	}
	// Restores lead; the focus restore trails. leaveFocus is a pane the user
	// may well have closed, and behind a select-pane that errors tmux drops
	// every option restore in the line.
	restore = leadWithRestores(restore, "select-pane -t "+q(leaveFocus))
	outs, errs := ctl.runPipelined(critical, restore)
	if errs[0] != nil {
		if tgt != nil && tgt.spacer != "" {
			// The spacer died under us (user closed it): forget the entry and
			// retry once as a first visit — tgt is nil then, no recursion.
			delete(p.carved, wid)
			log.Printf("scrub swap %s failed (%v), retrying as first visit", wid, errs[0])
			return d.dockMove(ctl, wid, focusMain, focusPane)
		}
		// Carve path: if the batch died after the split, don't leak the spacer.
		for _, ln := range outs[0] {
			if strings.HasPrefix(ln, "%") {
				_, _ = ctl.run("kill-pane -t " + q(ln))
			}
		}
		return errs[0]
	}
	// The spacer now filling the old window's slot: the target's, or the one
	// the carve just printed.
	spacerOld := ""
	if tgt != nil {
		spacerOld = tgt.spacer
	} else {
		for _, ln := range outs[0] {
			if strings.HasPrefix(ln, "%") {
				spacerOld = ln
				break
			}
		}
	}
	commit()
	if errs[1] != nil {
		log.Printf("scrub restore %s: %v", p.win, errs[1])
	}
	if spacerOld == "" {
		log.Printf("scrub: no spacer id for %s — release will not restore it", p.win)
	}
	p.carved[p.win] = &carveState{spacer: spacerOld, orig: p.snap.layout}
	delete(p.carved, wid)
	prev := p.win
	p.win, p.sess, p.snap = wid, sidN, snapN
	// After the state has moved, so the scrub's own plan is computed against
	// where the dock now IS. Run against the old state it would re-wrap the
	// session the move has just handed back.
	p.scrubWin = "" // the move plan already put row 0 back
	d.scrubEnd(ctl, false)
	d.lastScrub = time.Now()
	if bench {
		log.Printf("bench scrub %s -> %s focus_main=%v swap=%v", prev, wid, focusMain, tgt != nil)
	}
	return nil
}

// commitScrub lands a billboard scrub: on the docked window itself it is a
// free unzoom, anywhere else the geometry-free swap. Either way the origin
// resets — q now returns here.
//
// This used to hand off to a second TUI spawned in the target, because
// swapping the ZOOMED sidebar shrank its canvas-filled grid 480->26 and tmux
// rewrapped it into a wall of text. The alternate screen makes that shrink a
// CLIP instead, and the zoomed layout already paints the list in columns
// 1..listW, so the sidebar arrives showing exactly the list. The swap needs
// no second process, no phase-2 timer, and no window in which the client has
// moved but the sidebar has not painted.
func (d *daemon) commitScrub(ctl *control, wid string, focusPane string) error {
	p := d.dock
	if p == nil {
		return nil
	}
	if wid == "" || wid == p.win {
		d.scrubEnd(ctl, true)
		return d.dockCommit(ctl, focusPane)
	}
	if err := d.dockMove(ctl, wid, true, focusPane); err != nil {
		return err
	}
	p.originSess, p.originWin = p.sess, p.win
	d.pushSelect(selectMsg{Type: "select", Window: p.win})
	return nil
}

// dockNav is the routed M-h / M-l: previous/next window of the current
// session with the sidebar riding along atomically.
func (d *daemon) dockNav(ctl *control, dir string) error {
	p := d.dock
	if p == nil {
		return errors.New("nav while not docked")
	}
	var wins []window
	for _, w := range d.h.getWorld().Windows {
		if w.SessionID == p.sess {
			wins = append(wins, w)
		}
	}
	if len(wins) < 2 {
		return nil
	}
	cur := -1
	for i, w := range wins {
		if w.ID == p.win {
			cur = i
		}
	}
	if cur < 0 {
		return nil
	}
	step := 1
	if dir == "prev" {
		step = -1
	}
	target := wins[(cur+step+len(wins))%len(wins)].ID
	if err := d.dockMove(ctl, target, true, ""); err != nil {
		return err
	}
	d.pushSelect(selectMsg{Type: "select", Window: target})
	return nil
}

// dockCommit is Enter in the docked list: keep the sidebar, put the keyboard
// in the main area. The origin resets — q now returns here. A requested
// pane (an agent row's own pane) wins over the snapshot's active pane.
func (d *daemon) dockCommit(ctl *control, pane string) error {
	p := d.dock
	if p == nil {
		return nil
	}
	target := p.snap.activePane
	if pane != "" && pane != p.pane && d.paneInWindow(pane, p.win) {
		target = pane
	}
	alive := false
	for _, pn := range d.h.getWorld().Panes {
		if pn.ID == target && pn.WindowID == p.win {
			alive = true
			break
		}
	}
	if !alive {
		target = ""
		for _, pn := range d.h.getWorld().Panes {
			if pn.WindowID == p.win && pn.ID != p.pane {
				target = pn.ID
				break
			}
		}
	}
	if target == "" {
		return errors.New("no main pane to focus")
	}
	p.originSess, p.originWin = p.sess, p.win
	log.Printf("dock commit focus=%s win=%s", target, p.win)
	_, err := ctl.run("select-pane -t " + q(target))
	return err
}

// dockClose undocks. toOrigin (q) returns the client to where it was docked
// or last committed; otherwise (M-s) it stays put and the layout snaps back.
// Undock and restore ride ONE sequence: split across round trips, the
// window's apps see two resizes (kill-pane's give-all-to-the-neighbor, then
// the restore) and visibly jitter. The TUI pane is killed outright — its
// process dies with it, and the next dock spawns a fresh one.
func (d *daemon) dockClose(ctl *control, toOrigin bool) error {
	p := d.dock
	if p == nil {
		return nil
	}
	// State only: kill-pane below removes the sidebar even zoomed (killing
	// the zoomed pane unzooms), and an explicit early unzoom would flash the
	// origin panes before a toOrigin switch lands.
	d.scrubEnd(ctl, false)
	d.dock = nil
	d.pv.target = ""
	d.pv.reset()
	log.Printf("undock client=%s win=%s to_origin=%v", p.client, p.win, toOrigin)
	oldLayout, oldDirty, curActive := "", false, ""
	if lines, err := ctl.run("display-message -p -t " + q(p.win) + " -F " +
		f("#{window_layout}", "#{@winch_layout_dirty}", "#{pane_id}")); err == nil && len(lines) > 0 {
		if lp := strings.Split(lines[0], sep); len(lp) == 3 {
			oldLayout, oldDirty, curActive = lp[0], lp[1] == "1", lp[2]
		}
	}
	restore := d.leaveLayout(p.win, p.snap.layout, oldLayout, oldDirty, p.pane)
	// Focus after undock: whatever main pane the user is IN right now. Only
	// when the sidebar itself holds focus does the dock-time snapshot apply
	// — restoring the snapshot unconditionally yanked focus back to the
	// pane that happened to be active when the sidebar opened.
	focus := p.snap.activePane
	if curActive != "" && curActive != p.pane {
		focus = curActive
	}

	moving := toOrigin && p.originWin != p.win
	// Everything winch took, in one list. Held windows give their options back
	// here rather than at release: an option write costs nothing, while a
	// release stalls tmux reflowing scrollback and is deliberately deferred.
	undock := d.opts.releaseAll()
	if moving {
		// The unpad rides the SAME batch as the landing — a round trip later and
		// the status visibly flickers — but it leads the batch rather than
		// trailing it, so a kill-pane on a dead spacer or a switch to a session
		// that has gone cannot take it down with them.
		seq := leadWithRestores(undock)
		if t := p.carved[p.originWin]; t != nil && t.spacer != "" {
			// Landing on a spacer-held window: drop the spacer and replay the
			// original layout in the batch with the switch — one coalesced
			// reflow, and the window arrives already full width.
			oLay, oDirty := d.leaveInfo(ctl, p.originWin)
			seq = append(seq, "kill-pane -t "+q(t.spacer))
			if rl := d.leaveLayout(p.originWin, t.orig, oLay, oDirty, t.spacer); rl != "" {
				seq = append(seq, "select-layout -t "+q(p.originWin)+" "+q(rl))
			}
			seq = append(seq, "set-option -w -uq -t "+q(p.originWin)+" @winch_layout_dirty")
			delete(p.carved, p.originWin)
		}
		seq = append(seq,
			"select-window -t "+q(p.originWin),
			"switch-client -c "+q(p.client)+" -t "+q(p.originSess))
		if _, err := ctl.runSeq(seq...); err != nil {
			// Origin may have died; session alone, then keep cleaning.
			_, _ = ctl.run("switch-client -c " + q(p.client) + " -t " + q(p.originSess))
			for _, c := range undock {
				_, _ = ctl.run(c)
			}
		}
	}
	// Staying put: everything (undock, unpad, layout restore) in one batch so
	// the redraw coalesces. Leading with the restores again — kill-pane errors
	// whenever the sidebar pane has already gone, which is exactly the case
	// where the pad most needs removing.
	var seq []string
	if !moving {
		seq = leadWithRestores(undock)
	}
	seq = append(seq,
		"kill-pane -t "+q(p.pane),
		"set-option -w -uq -t "+q(p.win)+" @winch_layout_dirty")
	if restore != "" {
		seq = append(seq, "select-layout -t "+q(p.win)+" "+q(restore))
	}
	seq = append(seq, "select-pane -t "+q(focus))
	if _, err := ctl.runSeq(seq...); err != nil {
		log.Printf("undock: %v", err)
	}
	d.deferReleases(p)
	return nil
}

// leaveLayout picks what a window being released should get back: its exact
// pre-carve layout normally, or a proportional rescale of the CURRENT docked
// layout minus the given pane when it was deliberately reshaped while docked
// (@winch_layout_dirty). Empty means no restore (let tmux expand naturally).
func (d *daemon) leaveLayout(wid string, exact string, dockedLayout string, dirty bool, drop string) string {
	if !dirty {
		return exact
	}
	s, err := sansSidebar(dockedLayout, drop)
	if err != nil {
		log.Printf("give-back %s: %v", wid, err)
		return ""
	}
	return s
}

// padFlush is true exactly when the padded status row is the one ADJACENT to
// the pane area, which is the only case where a border glyph in it continues
// anything. status-left lives on status-format[0], and tmux draws status rows
// 0..N-1 top to bottom: at the bottom the block's first row is the one
// touching the panes, so any N works; at the top the LAST row touches them, so
// only N==1 does. `on` and `1` are the same value spelt two ways.
//
// Kept in the format rather than resolved in Go because both options are
// per-session and the user can change either without the daemon hearing.
const padFlush = "#{||:#{==:#{status-position},bottom}," +
	"#{||:#{==:#{status},on},#{==:#{status},1}}}"

// padBordered is whether that column holds a border at all. A scrub zooms the
// sidebar over the whole window and nothing re-runs dockSessionCmds for it, so
// the pad keeps its width while the border it was continuing is gone; the same
// goes for a content pane the user zooms with prefix-z, and for a window that
// is briefly down to one pane. Blank columns hid all of that. A glyph does not.
const padBordered = "#{&&:#{!=:#{window_panes},1},#{==:#{window_zoomed_flag},0}}"

// padWin names the option the pad gates on: the window holding the sidebar.
// Also how the pre-registry sweep recognises a winch-written format — nothing a
// theme emits references a winch option.
const padWin = "#{@winch_win}"

// padPrefix is the run of columns that pushes one status row past the sidebar:
// the sidebar's own width, then the border column.
//
// It is GATED on the window holding the sidebar. Status options are
// per-session while a sidebar is per-client, so an unconditional pad punches a
// hole in the bar of every other client on the session. Gating on @winch_win
// moves the question to the window, which is the one thing two clients agree
// on: same window, both see the sidebar and both want the shift; different
// window, neither does. No client tracking to keep in sync.
//
// No commas inside the branches. tmux splits a conditional at the first comma
// not inside #{}, and it does NOT count #[] — so a single #[bg=x,fg=y] would
// truncate the branch at that comma (probe-verified). Hence the separate
// style directives.
func padPrefix(width, row int) string {
	// bg=terminal (tmux >= 3.4) is the TERMINAL's default background — what
	// the sidebar paints on — so the strip above the sidebar reads as
	// sidebar, not statusline. (bg=default would NOT work: inside the
	// status line "default" means inherit status-style, i.e. the themed
	// statusline background — a no-op.)
	padBG := "terminal"
	if uiTheme != "terminal" {
		padBG = "#181825" // the sidebar's own ground (tui.go pal.bg, catppuccin)
	}
	inner := "#[bg=" + padBG + "]#[fg=" + padBG + "]" + strings.Repeat(" ", width) +
		padCell(width, row, padBG) + "#[default]"
	return "#{?#{==:#{window_id}," + padWin + "}," + inner + ",}"
}

// padCell is the pad's last column — the border column. It carries the border
// glyph rather than a space, or the sidebar's border would stop dead at the
// status row; on the default theme that is the only seam there is, since the
// sidebar ground, the pad and status-style's bg are all #181825.
//
// Only row 0 can hold it. Rows are drawn 0..N-1 top to bottom, so with the bar
// below the panes row 0 is the one that touches them, and with the bar above
// only a single-row bar has row 0 touching. padFlush is that test; on a
// multi-row bar at the top no row gets a glyph, which is honest rather than
// pointing one at a row of text.
//
// The bg is forced back after the border style because a style can reset it,
// and this cell belongs to the pad's ground either way.
func padCell(width, row int, padBG string) string {
	if row != 0 {
		return " "
	}
	// One style, matching the pinned border rather than asking tmux which of
	// the two it would use — that question is what the pin removes. Commas
	// become separate directives: tmux splits a conditional at the first comma
	// not inside #{}, and a style like "fg=red,bold" would truncate the branch.
	seam := "#[" + strings.ReplaceAll(uiSeamStyle, ",", "]#[") + "]"
	return "#{?#{&&:" + padFlush + "," + padBordered + "}," +
		seam + "#[bg=" + padBG + "]" + borderGlyph(uiBorderLines) +
		", }"
}

// borderStyle emits the style tmux paints a border segment in — with one
// rewrite that is the whole reason this function exists.
//
// tmux's stock pane-border-style is the literal `default`, and `default`
// inside a STATUS line does not mean what it means on a border: it resolves to
// status-style, the BAR's own colours (stock: fg=black). So applying the
// option verbatim painted the glyph in the statusbar's foreground, one row
// above a border drawn in the terminal's — dark on dark, and on the default
// theme indistinguishable from no glyph at all.
//
// fg=terminal (tmux >= 3.4) is the terminal's own foreground, which is exactly
// what a `default` border is drawn with. An option holding a real colour, or a
// format that computes one, is passed through untouched.
func borderStyle(opt string) string {
	unset := "#{||:#{==:#{" + opt + "},default},#{==:#{" + opt + "},}}"
	return "#[#{?" + unset + ",fg=terminal,#{E:" + opt + "}}]"
}

// The pad WRAPS the session's own status-format rather than setting
// status-left, and that is what makes it config-agnostic. status-left is the
// USER's content — powerline segments live there — and setting it deleted
// theirs for as long as the sidebar was docked. Worse, a theme that rewrites
// status-format and never interpolates status-left made the pad a silent no-op.
// A prefix survives the #[align=left] the stock format opens with
// (probe-verified) and shifts whatever is inside, including their status-left,
// without reading a byte of it. desiredOpts builds it; owned.go installs it.

// checkDock runs after every re-list: follow the client when it switched
// windows by a path we don't route (choose-tree, prefix keys), clean up when
// the client detached or the sidebar pane died, and keep the sidebar at its
// fixed width when a client resize rescaled it.
func (d *daemon) checkDock(ctl *control, w world) {
	p := d.dock
	if p == nil {
		return
	}
	if !paneAlive(w, p.pane) {
		// Sidebar pane died (kill-window on the host, kill-pane): the dock is
		// gone, only session state needs cleaning. Layout restore would fight
		// whatever the user just did — skip it.
		log.Printf("dock: sidebar pane gone, cleaning up")
		d.dock = nil
		d.stopStream()
		d.pv.target = ""
		d.pv.reset()
		if seq := d.opts.releaseAll(); len(seq) > 0 {
			if _, err := ctl.runSeq(seq...); err != nil {
				log.Printf("dock cleanup: %v", err)
			}
		}
		d.deferReleases(p)
		return
	}
	var cl *tclient
	for i := range w.Clients {
		if w.Clients[i].Name == p.client {
			cl = &w.Clients[i]
			break
		}
	}
	if cl == nil {
		log.Printf("dock: client %s detached, undocking", p.client)
		_ = d.dockClose(ctl, false)
		return
	}
	if p.scrubbing {
		for _, pn := range w.Panes {
			if pn.ID == p.pane && pn.WindowID == p.win && pn.Width == d.width() {
				// The zoom broke externally: selecting any other pane
				// (vim-navigator C-h/C-l out of the billboard) auto-unzooms.
				// Reality is the docked window again — end the scrub state
				// and snap the list highlight back, or the next j/k would
				// stream billboards at a 40-col TUI that can't paint them.
				log.Printf("dock: scrub unzoomed externally, ending")
				d.scrubEnd(ctl, false)
				d.pushSelect(selectMsg{Type: "select", Window: p.win})
				break
			}
		}
	}
	cur := ""
	for _, win := range w.Windows {
		if win.SessionID == cl.SessionID && win.Active {
			cur = win.ID
			break
		}
	}
	if cur != "" && cur != p.win {
		log.Printf("dock: follow %s -> %s (unrouted switch)", p.win, cur)
		if err := d.dockMove(ctl, cur, true, ""); err != nil {
			log.Printf("dock follow: %v", err)
			return
		}
		d.pushSelect(selectMsg{Type: "select", Window: cur})
		return
	}
	if p.scrubbing {
		return // zoomed: full-width by design; enforcing width would unzoom it
	}
	// Width: a border drag leaves the WINDOW width alone — adopt the new
	// pane width as the user's choice (and resize the spacers to match). A
	// client/window resize can drift the pane too; there the chosen width
	// gets re-asserted instead.
	winW := p.hostW
	for _, win := range w.Windows {
		if win.ID == p.win {
			if lw := layoutWidth(win.Layout); lw > 0 {
				winW = lw
			}
			break
		}
	}
	for _, pn := range w.Panes {
		if pn.ID != p.pane || pn.WindowID != p.win {
			continue
		}
		switch {
		case winW != p.hostW:
			p.hostW = winW
			if pn.Width != d.width() {
				_, _ = ctl.run(fmt.Sprintf("resize-pane -t %s -x %d", q(p.pane), d.width()))
			}
		case pn.Width != d.width() && pn.Width >= 18 && pn.Width <= 80:
			log.Printf("dock: adopted width %d (border drag)", pn.Width)
			d.setWidth(ctl, pn.Width, false)
		case pn.Width != d.width():
			_, _ = ctl.run(fmt.Sprintf("resize-pane -t %s -x %d", q(p.pane), d.width()))
		}
		break
	}
}

// setWidth applies a new sidebar width: spacers and (unless the change
// came from the pane itself) the sidebar pane resize to match, the status
// pad re-shifts, and the TUI is told so its layout math follows.
func (d *daemon) setWidth(ctl *control, wpx int, resizePane bool) {
	if wpx < 18 {
		wpx = 18
	}
	if wpx > 80 {
		wpx = 80
	}
	if wpx == d.width() {
		return
	}
	d.dockW = wpx
	d.h.setWidth(wpx) // a TUI spawned later lays out at this width from birth
	saveOpt(ctl, optWidth, strconv.Itoa(wpx))
	if p := d.dock; p != nil {
		for _, t := range p.carved {
			if t.spacer != "" {
				_, _ = ctl.run(fmt.Sprintf("resize-pane -t %s -x %d", q(t.spacer), wpx))
			}
		}
		if resizePane && !p.scrubbing {
			_, _ = ctl.run(fmt.Sprintf("resize-pane -t %s -x %d", q(p.pane), wpx))
		}
		// The pad's width is part of the desired value, so re-planning is the
		// whole of it — including the scrub override, which is padded like any
		// other row.
		d.applyDockPlan(ctl, p)
	}
	d.h.sendRole("list", marshalLine(widthMsg{Type: "width", Width: wpx}))
}

func paneAlive(w world, pid string) bool {
	for _, p := range w.Panes {
		if p.ID == pid {
			return true
		}
	}
	return false
}
