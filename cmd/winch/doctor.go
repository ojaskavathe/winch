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
	for _, o := range []string{optTheme, optWidth, optSplit, optSeam} {
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
	if w := staleBindWarning(); w != "" {
		r.check(false, "the M-s bind runs the installed build", strings.Split(w, "\n")[1:]...)
	} else {
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
	if got, err := ctl.run("list-panes -a -F " + f("#{pane_id}", "#{@winch_sidebar}", "#{pane_start_command}")); err == nil {
		for _, ln := range got {
			c := strings.Split(ln, sep)
			if len(c) != 3 {
				continue
			}
			if c[1] == "1" && (p == nil || c[0] != p.pane) {
				orphanPanes = append(orphanPanes, c[0]+" is a sidebar pane the daemon does not own")
			}
			if strings.Trim(c[2], `"`) == spacerCmd && !spacerHeld(p, c[0]) {
				orphanPanes = append(orphanPanes, c[0]+" is a spacer no carve is holding")
			}
		}
	}
	r.check(len(orphanPanes) == 0, "no orphan sidebar panes or spacers", orphanPanes...)

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
