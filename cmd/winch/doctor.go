package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// stripSGR removes CSI SGR colour sequences (\x1b[ … m) so a styled row can
// be shown or compared as the plain text it paints.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// `winch doctor` — one command that answers "what is winch doing to my tmux
// right now, and does any of it disagree with itself".
//
// It exists because diagnosing the sidebar meant twenty hand-written
// show-options calls, and because the interesting failures are DIVERGENCES that
// no single read can show: an option winch wrote with no claim recorded against
// it, a claim recorded for an option nobody wrote, a session flagged docked
// that the daemon has never heard of. Each of those is a restore that went
// missing, and each is invisible from either side alone — the daemon's memory
// looks consistent, the server's options look plausible, and only the pair is
// wrong.
//
// So the checks compare the two. Everything above them is context for reading
// the answer.
//
// Deliberately read-only. A tool you reach for when something looks broken must
// not be a tool that changes what you were about to look at.

type report struct {
	lines []string
	bad   int
}

func (r *report) add(format string, a ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, a...))
}

func (r *report) blank() { r.lines = append(r.lines, "") }

func (r *report) head(s string) {
	r.blank()
	r.add("%s", s)
}

// check records a pass or a failure. detail is printed only when it fails —
// a clean report should be short enough to read at a glance, and a failing one
// should say enough to act on without a second command.
func (r *report) check(ok bool, name string, detail ...string) {
	if ok {
		r.add("  ok    %s", name)
		return
	}
	r.bad++
	r.add("  FAIL  %s", name)
	for _, d := range detail {
		r.add("          %s", d)
	}
}

// winchWritten reports whether a status-format is winch's own work. Both marks
// are things no theme emits: the pad references a winch option, the scrub
// override runs a filtered session loop.
func winchWritten(v string) string {
	switch {
	case strings.Contains(v, scrubFmtMark):
		return "scrub override"
	case strings.Contains(v, padWin):
		return "pad"
	}
	return ""
}

func (d *daemon) doctor(ctl *control) []string {
	r := &report{}
	r.add("winch doctor")

	// ---- build ----------------------------------------------------------
	r.head("build")
	exe, _ := os.Executable()
	r.add("  daemon    %s", exe)
	if v, err := ctl.run("display-message -p '#{version}'"); err == nil && len(v) == 1 {
		r.add("  tmux      %s", v[0])
	}
	r.add("  socket    %s", d.tmuxSock)
	r.add("  log       %s.log", winchSocketPath(d.tmuxSock))

	// ---- config ---------------------------------------------------------
	r.head("config")
	for _, o := range []string{optTheme, optWidth, optSplit, optSeam, optNav,
		optNotify, optNotifyOSC, optNotifyVia, optNotifyDelay} {
		v := optStr(ctl, o)
		if v == "" {
			v = "(unset)"
		}
		r.add("  %-22s %s", o, v)
	}
	r.add("  %-22s %d", "width in use", d.width())
	r.add("  %-22s %q  (glyph %s)", "seam style", uiSeamStyle, borderGlyph(uiBorderLines))
	r.add("  %-22s %s", "pane-border-lines", uiBorderLines)
	r.add("  %-22s %v", "alternate-screen", altScreen)
	// The keys actually in force, which is the interesting number: unset
	// means detected-from-your-binds, and what it detected is not otherwise
	// visible anywhere.
	r.add("  %-22s %s", "nav keys in use", uiNav)
	if det := detectNavKeys(ctl).resolved(); det != (navKeys{}) {
		r.add("  %-22s %s", "detected from binds", det)
	}
	// Notifications leave the tmux server entirely, so the only thing worth
	// reporting here is what winch WOULD send and where — whether the
	// terminal on the far end acts on it is what `winch notify-test` is for.
	r.add("  %-22s %s", "notifications", loadNotifyCfg(ctl))
	// Asked of tmux rather than the cached world: this is the address a
	// notification would actually be written to, and a stale one reads as
	// working right up until it silently is not.
	// focus-events gates the suppression rule entirely: with it off tmux never
	// asks the terminal to report focus, every client reads as focused
	// forever, and winch stays quiet about the window you are on even when you
	// have alt-tabbed away from the terminal completely. Worth saying out
	// loud, because the feature fails by doing nothing.
	fe := "off"
	if v, err := ctl.run("show-options -gqv focus-events"); err == nil && len(v) == 1 {
		fe = v[0]
	}
	note := ""
	if fe != "on" {
		note = "  (agents in the window you are on stay quiet even when the terminal is unfocused)"
	}
	r.add("  %-22s %s%s", "focus-events", fe, note)
	if cls, err := ctl.run("list-clients -F " + f("#{client_name}", "#{client_control_mode}",
		"#{client_tty}", "#{client_termname}", "#{client_flags}")); err == nil {
		for _, ln := range cls {
			p := strings.Split(ln, sep)
			if len(p) != 5 || p[1] == "1" {
				continue
			}
			// A client's NAME is usually its tty path, so printing both
			// reads as a bug rather than a fact.
			label := "notify " + p[0]
			if p[0] == p[2] {
				label = "notify client"
			}
			focus := "unfocused"
			if strings.Contains(","+p[4]+",", ",focused,") {
				focus = "focused"
			}
			r.add("  %-22s %s  TERM=%s  OSC %s  %s", label, p[2], p[3],
				loadNotifyCfg(ctl).resolveOSC(p[3]), focus)
		}
	}

	// ---- dock -----------------------------------------------------------
	r.head("dock")
	p := d.dock
	if p == nil {
		r.add("  not docked")
	} else {
		r.add("  client    %s", p.client)
		r.add("  session   %s", p.sess)
		r.add("  window    %s", p.win)
		r.add("  pane      %s", p.pane)
		r.add("  origin    %s / %s", p.originSess, p.originWin)
		r.add("  scrubbing %v", p.scrubbing)
		if p.scrubWin != "" {
			r.add("  bar shows %s (scrub override)", p.scrubWin)
		}
		held := dockHeld(p)
		sort.Strings(held)
		r.add("  held      %s", strings.Join(held, " "))
		// The pad's own conditionals, as tmux evaluates them THIS INSTANT for
		// the docked window. A pad that is installed and correct still draws
		// nothing when either of these is false, which looks identical to a
		// pad that was never installed.
		if got, err := ctl.run("display-message -p -t " + q(p.win) + " -F " +
			f("#{"+padFlushExpr+"}", "#{"+padBorderedExpr+"}", "#{window_panes}", "#{window_zoomed_flag}",
				"#{status}", "#{status-position}")); err == nil && len(got) == 1 {
			c := strings.Split(got[0], sep)
			if len(c) == 6 {
				r.add("  padFlush=%s padBordered=%s  (panes=%s zoomed=%s status=%s pos=%s)",
					c[0], c[1], c[2], c[3], c[4], c[5])
			}
		}
		// Does the seam agree with the border it continues? Both are pinned on
		// the sidebar pane, and tmux reads BOTH from the pane that owns the
		// border (screen_redraw_draw_borders_style), so they should be equal to
		// each other and to the style the glyph is painted in.
		if got, err := ctl.run("display-message -p -t " + q(p.pane) + " -F " +
			f("#{pane-border-style}", "#{pane-active-border-style}", "#{pane-border-indicators}")); err == nil && len(got) == 1 {
			c := strings.Split(got[0], sep)
			if len(c) == 3 {
				r.add("  sidebar pane border: normal=%q active=%q indicators=%s", c[0], c[1], c[2])
			}
		}
	}

	// ---- agents ---------------------------------------------------------
	// What the sidebar ACTUALLY renders for every detected agent, beside the
	// raw material it renders from. The card is built in the TUI, so a bug
	// between the pane's title/state and the row you see (a title sliced at
	// its own " · ", a stale reason, a mis-styled token) is otherwise
	// invisible from here — you would need a screenshot to catch it. This
	// runs the real render (uiAgentRows + fitAgentRow) against the daemon's
	// world so `winch doctor` shows the row exactly as painted.
	r.head("agents")
	{
		w := d.h.getWorld()
		st := &store{
			sessions: map[string]session{},
			windows:  map[string]window{},
			panes:    map[string]pane{},
		}
		for _, s := range w.Sessions {
			st.sessions[s.ID] = s
		}
		for _, wd := range w.Windows {
			st.windows[wd.ID] = wd
		}
		for _, p := range w.Panes {
			st.panes[p.ID] = p
		}
		// A generous width so legitimate truncation does not hide a render
		// bug: at this width the row should be the raw title verbatim, so any
		// divergence is the render's own doing.
		const availDoc = 60
		n := 0
		for _, p := range w.Panes {
			if p.Agent == "" {
				continue
			}
			n++
			r.add("  %s %-7s state=%-10s title=%q", p.ID, p.Agent, orDash(p.AgentState), p.Title)
			if p.AgentReason != "" {
				r.add("      reason %q", p.AgentReason)
			}
			for ri, spec := range uiAgentRows.rows {
				vals := uiAgentRows.values(spec, st, p)
				if len(vals) == 0 {
					continue
				}
				// The SGR-styled second return is what paintList writes to
				// the screen; the first (plain) return is a measurement copy
				// and does not always share the styled string's text (a bug
				// can corrupt one and not the other). Show what is actually
				// painted, with the colour codes stripped.
				_, styled := fitAgentRow(vals, availDoc, ri == 0, pal.subtext)
				r.add("      card  %q", strings.TrimSpace(stripSGR(styled)))
			}
		}
		if n == 0 {
			r.add("  no agent panes detected")
		}
	}

	// ---- the bar, per session -------------------------------------------
	r.head("status line")
	type sessRow struct {
		id, name, docked, win string
		fmt0                  string
		mark                  string
	}
	var rows []sessRow
	if got, err := ctl.run("list-sessions -F " + f("#{session_id}", "#{session_name}",
		"#{@winch_docked}", "#{@winch_win}", "#{"+markName("status-format")+"}")); err == nil {
		for _, ln := range got {
			c := strings.Split(ln, sep)
			if len(c) != 5 {
				continue
			}
			row := sessRow{id: c[0], name: c[1], docked: c[2], win: c[3], mark: c[4]}
			if v, e := ctl.run("show-options -q -t " + q(row.id) + " status-format"); e == nil {
				row.fmt0 = strings.Join(v, "\n")
			}
			rows = append(rows, row)
		}
	}
	for _, row := range rows {
		kind := winchWritten(row.fmt0)
		switch {
		case kind == "" && row.fmt0 == "":
			kind = "(inherits the global)"
		case kind == "":
			kind = "the user's own"
		}
		r.add("  %-4s %-16s %s", row.id, row.name, kind)
		if row.docked != "" || row.win != "" {
			r.add("         docked=%q win=%q mark=%q", row.docked, row.win, row.mark)
		}
		if kind == "scrub override" {
			if t := scrubTarget(row.fmt0); t != "" {
				r.add("         rendering %s — NOT this session", t)
			}
		}
	}

	// ---- checks ---------------------------------------------------------
	r.head("checks")
	// Read the BIND, not this process. staleBindWarning compares
	// os.Executable() to the profile, which is exactly right when a bind
	// invokes a stale winch and it warns on its own behalf — but useless
	// here, because doctor is normally run AS the installed binary, so it
	// compared the profile to itself and passed while the bind was stale.
	// Observed 2026-09-01: "all checks passed" on a server whose M-s still
	// pointed at the previous generation. tmux never re-reads its config, so
	// this is the normal state after every rebuild and the check that claims
	// to catch it has to actually look.
	bind, prof := bindStorePath(ctl), installedStorePath()
	switch {
	case bind == "" || prof == "":
		r.check(true, "the M-s bind runs the installed build",
			"not comparable (no nix store path on one side)")
	case bind != prof:
		r.check(false, "the M-s bind runs the installed build",
			"bind:      "+bind,
			"installed: "+prof,
			"fix: tmux -S "+d.tmuxSock+" source-file ~/.config/tmux/tmux.conf")
	default:
		r.check(true, "the M-s bind runs the installed build")
	}

	// The one that matters most: winch's writing and winch's record of it must
	// agree. A format with no mark is a restore that was dropped — nothing will
	// ever put that session's bar back, because nothing knows it is owed.
	var unmarked, orphanMarks, ghostDocked []string
	for _, row := range rows {
		if kind := winchWritten(row.fmt0); kind != "" && row.mark == "" {
			unmarked = append(unmarked, fmt.Sprintf("%s %s holds a winch %s with no claim mark",
				row.id, row.name, kind))
		}
		if row.mark != "" && winchWritten(row.fmt0) == "" {
			orphanMarks = append(orphanMarks, fmt.Sprintf("%s %s is marked but holds no winch format",
				row.id, row.name))
		}
		if row.docked != "" && (p == nil || row.id != p.sess) {
			ghostDocked = append(ghostDocked, fmt.Sprintf("%s %s is flagged docked; the sidebar is %s",
				row.id, row.name, dockWhere(p)))
		}
	}
	r.check(len(unmarked) == 0, "every winch-written status format carries a claim mark", unmarked...)
	r.check(len(orphanMarks) == 0, "no claim mark outlives what it claimed", orphanMarks...)
	r.check(len(ghostDocked) == 0, "only the docked session is flagged docked", ghostDocked...)

	// The registry's memory against the server's marks, both ways.
	server := map[optKey]bool{}
	for _, k := range ownedOptions {
		list, idVar := "list-sessions", "#{session_id}"
		if k.scope == scopeWindow {
			list, idVar = "list-windows -a", "#{window_id}"
		}
		got, err := ctl.run(list + " -F " + f(idVar, "#{"+markName(k.name)+"}"))
		if err != nil {
			continue
		}
		for _, ln := range got {
			c := strings.Split(ln, sep)
			if len(c) == 2 && c[1] != "" {
				kk := k
				kk.target = c[0]
				server[kk] = true
			}
		}
	}
	var onlyMem, onlySrv []string
	for k := range d.opts.own {
		if !server[k] {
			onlyMem = append(onlyMem, k.String()+" — claimed in memory, unmarked on the server")
		}
	}
	for k := range server {
		if !d.opts.owns(k) {
			onlySrv = append(onlySrv, k.String()+" — marked on the server, not claimed in memory")
		}
	}
	sort.Strings(onlyMem)
	sort.Strings(onlySrv)
	r.check(len(onlyMem) == 0 && len(onlySrv) == 0,
		fmt.Sprintf("registry agrees with the server (%d claimed, %d marked)", len(d.opts.own), len(server)),
		append(onlyMem, onlySrv...)...)

	// Panes winch is responsible for that nothing is holding.
	var orphanPanes []string
	live := map[string]bool{}
	if got, err := ctl.run("list-panes -a -F " + f("#{pane_id}", "#{@winch_sidebar}", "#{pane_start_command}")); err == nil {
		for _, ln := range got {
			c := strings.Split(ln, sep)
			if len(c) != 3 {
				continue
			}
			live[c[0]] = true
			if c[1] == "1" && (p == nil || c[0] != p.pane) {
				orphanPanes = append(orphanPanes, c[0]+" is a sidebar pane the daemon does not own")
			}
			if strings.Trim(c[2], `"`) == spacerCmd && !spacerHeld(p, c[0]) {
				orphanPanes = append(orphanPanes, c[0]+" is a spacer no carve is holding")
			}
		}
	}
	r.check(len(orphanPanes) == 0, "no orphan sidebar panes or spacers", orphanPanes...)

	// And the CONVERSE, which the check above cannot see: a carve whose
	// spacer has gone. The two directions fail for opposite reasons — a
	// stray spacer is litter nobody will collect, a missing one is a
	// promise winch can no longer keep. Only the first was checked, so a
	// daemon holding carves over dead panes reported itself perfectly
	// healthy right up until the undock, where the release replays a layout
	// against a window that no longer has the pane count it was saved with:
	// that is what `have N panes but need M` in the log below is.
	//
	// Costs nothing — the pane set was already listed above.
	lost := lostSpacers(p, live)
	r.check(len(lost) == 0, "every carve still has its spacer", lost...)

	// A window winch normalized is pinned to `window-size manual` by
	// resize-window until something unsets it, and a pinned window can NEVER
	// resize again. Nothing surfaces that: it looks perfect until the
	// CLIENT's size changes, and then every pinned window renders at the
	// geometry of a monitor that is no longer attached. Unplugging one on
	// 2026-09-01 left three windows at 480x95 on a 230x68 client with every
	// other check green — which is the whole argument for this check: the
	// failure is invisible on the machine that has it until the one moment
	// you cannot debug comfortably.
	//
	// Only a fault when the GLOBAL is not manual. Someone who set that
	// deliberately wants exactly this, and inheriting their choice is not
	// something to report.
	var pinned []string
	gv, gerr := ctl.run("show -gv window-size")
	if gerr == nil && (len(gv) == 0 || strings.TrimSpace(gv[0]) != "manual") {
		if got, err := ctl.run("list-windows -a -F " + f(
			"#{window_id}", "#{session_name}:#{window_index}",
			"#{window-size}", "#{window_width}x#{window_height}")); err == nil {
			for _, ln := range got {
				c := strings.Split(ln, sep)
				if len(c) == 4 && c[2] == "manual" {
					pinned = append(pinned, fmt.Sprintf(
						"%s (%s) is pinned at %s and can no longer resize", c[1], c[0], c[3]))
				}
			}
		}
	}
	if len(pinned) > 0 {
		pinned = append(pinned,
			"fix: tmux -S "+d.tmuxSock+" set -w -t <window> -u window-size")
	}
	r.check(len(pinned) == 0, "no window is pinned to a manual size", pinned...)

	// ---- recent errors on the paths that give things back ---------------
	if fails := recentRestoreFailures(d.tmuxSock, 6); len(fails) > 0 {
		r.head("recent tmux errors on leave / undock (from the log)")
		for _, ln := range fails {
			r.add("  %s", ln)
		}
		r.add("  these are cosmetic as long as the checks above pass: option")
		r.add("  restores lead those batches, so what fails behind them is a")
		r.add("  focus or layout restore. `scrub restore` lines older than the")
		r.add("  leadWithRestores fix DID strand the session's bar.")
	}

	r.blank()
	if r.bad == 0 {
		r.add("all checks passed")
	} else {
		r.add("%d check(s) failed", r.bad)
	}
	return r.lines
}

func dockWhere(p *dockState) string {
	if p == nil {
		return "not docked"
	}
	return "in " + p.sess
}

// lostSpacers names every carve whose spacer pane is gone from `live`.
//
// A separate function because reapEmptyCarves normally repairs this within a
// tick, so the rig can never catch the check firing in situ — it can only
// watch the recovery. Testing the predicate directly is the only way to know
// the backstop works rather than merely stays quiet, and "stays quiet" is
// indistinguishable from "is broken" from the outside.
func lostSpacers(p *dockState, live map[string]bool) []string {
	if p == nil {
		return nil
	}
	var out []string
	for wid, t := range p.carved {
		if t.spacer == "" {
			continue // the sidebar itself is in this window; no spacer is right
		}
		if !live[t.spacer] {
			out = append(out, fmt.Sprintf("%s is held by carve %s, which no longer exists", wid, t.spacer))
		}
	}
	sort.Strings(out) // map order, and this goes into a report
	return out
}

func spacerHeld(p *dockState, pid string) bool {
	if p == nil {
		return false
	}
	for _, t := range p.carved {
		if t.spacer == pid {
			return true
		}
	}
	return false
}

// scrubTarget pulls the session a leaked scrub override is looping over out of
// its own format, so the report can say what the bar is actually describing
// rather than just that it is wrong.
func scrubTarget(v string) string {
	i := strings.Index(v, scrubFmtMark)
	if i < 0 {
		return ""
	}
	rest := v[i+len(scrubFmtMark)-1:] // keep the leading $
	end := strings.IndexAny(rest, "},")
	if end <= 1 {
		return ""
	}
	return "session " + rest[:end]
}

// recentRestoreFailures greps the daemon log for the shape a dropped restore
// leaves: a batch that carried option restores coming back with a tmux error.
func recentRestoreFailures(tmuxSock string, max int) []string {
	b, err := os.ReadFile(winchSocketPath(tmuxSock) + ".log")
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if !strings.Contains(ln, "tmux: ") {
			continue
		}
		if strings.Contains(ln, "scrub restore") || strings.Contains(ln, "focus restore") ||
			strings.Contains(ln, "undock:") || strings.Contains(ln, "option plan") ||
			strings.Contains(ln, "dock cleanup") || strings.Contains(ln, "release ") {
			out = append(out, strings.TrimPrefix(ln, "winch: "))
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// padFlushExpr / padBorderedExpr are the pad's conditionals without the #{}
// wrapper, so the report can ask tmux to evaluate them directly.
var padFlushExpr = strings.TrimSuffix(strings.TrimPrefix(padFlush, "#{"), "}")
var padBorderedExpr = strings.TrimSuffix(strings.TrimPrefix(padBordered, "#{"), "}")

// cmdDoctor is the CLI entry point.
func cmdDoctor(tmuxSock, winchSock string) {
	conn, err := dialEnsure(tmuxSock, winchSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "winch doctor: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	b, _ := json.Marshal(cmdMsg{Type: "cmd", Cmd: "doctor"})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "winch doctor: %v\n", err)
		os.Exit(1)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m wireMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "reply" {
			continue
		}
		for _, ln := range m.Text {
			fmt.Println(ln)
		}
		if m.OK != nil && !*m.OK {
			fmt.Fprintf(os.Stderr, "winch doctor: %s\n", m.Err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "winch doctor: no reply from daemon")
	os.Exit(1)
}
