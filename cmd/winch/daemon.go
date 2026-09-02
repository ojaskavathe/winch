package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var bench = os.Getenv("WINCH_BENCH") != ""

// testFast, set only by the rig harness, compresses pure wall-clock
// constants (discovery tick, startup grace, frame TTL) so tests don't
// sleep through them. It never changes behavior, only timing.
var testFast = os.Getenv("WINCH_TEST_FAST") != ""

// daemon is the single-threaded core: every field is touched only from the
// consume loop, so none of it needs locking.
type daemon struct {
	tmuxSock string
	// hostName is tmux's #{host}, which is also its DEFAULT pane_title:
	// a pane whose program never set a title reports the hostname, and
	// that is the absence of a name, not a name.
	hostName string
	h        *hub

	pv   previewState // billboard capture engine (preview.go)
	det  detectState  // agent state detection (detect.go)
	dock *dockState   // docked sidebar mode (dock.go); nil = idle

	// lastScrub gates re-lists: world churn within scrubQuiet of a dock
	// scrub is our own doing, and re-listing the whole server once per
	// scrub step is the daemon's main cost during a held-key scrub.
	lastScrub time.Time

	// pendingRelease: spacer-held windows awaiting restore after an undock.
	// Drained one per releaseTick from the event loop (never inline with
	// the undock — the release stalls tmux while it reflows scrollback, and
	// inline that stall lands before the transition has painted). A re-dock
	// adopts the queue back instead.
	pendingRelease []releaseItem
	releaseT       *time.Timer
	releaseC       <-chan time.Time

	// shadow: the hidden layout-oracle window emulated billboards ask for
	// exact carve geometry (shadow.go).
	shadow shadowState

	// focusT/focusC: the delayed focus handoff after a dock open. A
	// focus-out landing within ~50ms of the open's resize throws Claude
	// Code panes into a render storm (~760 presented frames, probed), so
	// hello arms this timer instead of selecting the sidebar directly; the
	// fire-time guards skip a dock that has moved, begun scrubbing, or
	// closed since — those placed focus deliberately.
	focusT    *time.Timer
	focusC    <-chan time.Time
	focusPane string

	// closeT/closeC/pendingClose: a two-phase undock's deferred second
	// half (dock.go) — focus first, kill+widen dockFocusDelay later, so a
	// Claude Code pane never gets focus-in and resize in the same instant.
	closeT       *time.Timer
	closeC       <-chan time.Time
	pendingClose *pendingClose

	// opts owns every option winch takes from the user (owned.go): what each
	// one held before it was claimed, what winch last wrote over it, and the
	// commands to put it back. Nothing else in the daemon writes an option
	// that belongs to somebody else.
	opts *owner

	// git: per-session repo identity cache; gitC ticks the slow repoll
	// (git.go). The ticker lives for the daemon's lifetime, across
	// control-mode reconnects.
	git  map[string]gitInfo
	gitC <-chan time.Time

	// dockW: user-tuned sidebar width (0 = listWidth default). Survives
	// undocks; runtime-only for now.
	dockW int
}

// clientView: the client's current session, window, and size, from
// list-clients rows (each row expands in that client's own context).
func (d *daemon) clientView(ctl *control, client string) (sid, wid string, cw, ch int, err error) {
	lines, err := ctl.run("list-clients -F " + f("#{client_name}", "#{session_id}", "#{window_id}", "#{client_width}", "#{client_height}"))
	if err != nil {
		return "", "", 0, 0, err
	}
	for _, ln := range lines {
		p := strings.Split(ln, sep)
		if len(p) == 5 && p[0] == client {
			cw, _ = strconv.Atoi(p[3])
			ch, _ = strconv.Atoi(p[4])
			return p[1], p[2], cw, ch, nil
		}
	}
	names := make([]string, 0, len(lines))
	for _, ln := range lines {
		if p := strings.Split(ln, sep); len(p) == 5 {
			names = append(names, p[0])
		}
	}
	return "", "", 0, 0, fmt.Errorf("no client %s (server has: %s)", client, strings.Join(names, ", "))
}

// uiTheme is the @winch-theme option read once at attach; rides every
// snapshot so the TUI paints in the right palette from its first frame.
var uiTheme string

// uiAgentRowsRaw is @winch-agent-rows verbatim, and uiAgentRows is it
// parsed. The daemon carries the RAW string because the TUI is a separate
// process and parsing belongs where the rendering is — the daemon only
// validates so a typo is reported to the log with the daemon's timestamps
// rather than discovered as a card with a hole in it.
var uiAgentRowsRaw string

// uiAgentRows is set in the daemon from tmux and in the TUI from the
// snapshot that carries it, same as uiNav.
var uiAgentRows = defaultAgentRows()

// uiBorderLines is tmux's own pane-border-lines, read at attach so the status
// pad can end in the same glyph tmux draws down the sidebar's border column.
// Same lifetime as uiTheme: changing it mid-session needs a daemon restart.
var uiBorderLines string

// uiNav is the sidebar's pane-navigation keys, resolved at attach from
// @winch-nav-keys or from the user's own root bindings (config.go). Set in the
// daemon from tmux and in the TUI from the snapshot that carries it — same
// variable, two processes, filled from whichever source that process has.
var uiNav = navDefault

// uiSeamStyle is the colour of the sidebar's whole edge — the pane border down
// the side and the glyph continuing it into the status bar.
//
// It exists because tmux dims that border whenever focus is not on the
// sidebar, which is nearly always: the divider is the sidebar's own RIGHT
// border, so it follows the sidebar's activity, not the neighbour's. A
// 32-row line reads fine dim; the single cell in the status row reads as
// missing. Rather than have the glyph disagree with the border, the border is
// pinned — per PANE, so the rest of the window keeps its active highlight —
// and the glyph is painted the same. @winch-seam-style overrides it.
var uiSeamStyle string

// altScreen records whether tmux honours the TUI's alternate-screen switch
// (the `alternate-screen` window option, on by default). It decides how a
// scrub can be left: with the alternate screen the pane's grid is CLIPPED on
// the 480->26 shrink, so unzooming keeps the already-painted list; without
// it tmux reflows the wide canvas into the strip and the pane has to be
// respawned to clear the grid first.
var altScreen = true

// debounce for notification bursts: long enough to coalesce a storm (a
// session teardown emits dozens), short enough to be imperceptible.
const debounce = 15 * time.Millisecond

// scrubQuiet suppresses re-lists while dock moves are landing: each scrub
// step mutates the world, and one whole-server re-list per keystroke is
// wasted heat. The world settles this long after the last scrub. Bookkeeping
// only — key handling and painting never wait on this.
const scrubQuiet = 150 * time.Millisecond

func runDaemon(tmuxSock, winchSock string) {
	log.SetPrefix("winch: ")
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if exe, err := os.Executable(); err == nil {
		log.Printf("build %s", exe)
	}

	h := newHub()
	d := &daemon{tmuxSock: tmuxSock, h: h, opts: newOwner()}
	var ln net.Listener

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	gitT := time.NewTicker(gitTick)
	defer gitT.Stop()
	d.gitC = gitT.C

	for attempt := 0; ; attempt++ {
		ctl, err := dialControl(tmuxSock)
		if err != nil {
			log.Fatalf("spawn tmux: %v", err)
		}
		// tmux <= 3.5 octal-escapes non-printable bytes in control-mode
		// command replies: the \x1f field separator arrives as the text
		// "\037" (and captured SGR as "\033[...]"), so every listing parses
		// to an empty world. Fail loudly instead.
		if lines, perr := ctl.run("display-message -p '" + sep + "'"); perr == nil &&
			len(lines) == 1 && lines[0] == `\037` {
			ctl.close()
			log.Fatalf("tmux at %s escapes control-mode output; winch needs tmux >= 3.6", tmuxSock)
		}
		// User config lives in tmux global options, so it survives a daemon
		// restart (every deploy killed the dragged width before this) and can
		// be preset in tmux.conf. Read once per attach; writes go back through
		// the same keys. Convention: @winch-<name> with a HYPHEN is user
		// config, @winch_<name> with an underscore is daemon runtime state.
		d.loadConfig(ctl)
		// tmux's default pane_title, so titles equal to it mean "unset".
		if v, herr := ctl.run("display-message -p '#{host}'"); herr == nil && len(v) == 1 {
			d.hostName = v[0]
		}
		if lines, aerr := ctl.run("show-options -gwqv alternate-screen"); aerr == nil && len(lines) == 1 {
			altScreen = strings.TrimSpace(lines[0]) != "off"
			if !altScreen {
				log.Printf("alternate-screen off: unzoom falls back to respawn")
			}
		}
		w, err := fetchWorld(ctl)
		if err != nil {
			// Attach failed (server gone, or no sessions left). First attempt
			// is a startup error; afterwards it is the normal end of life.
			ctl.close()
			if attempt == 0 {
				log.Fatalf("attach %s: %v", tmuxSock, err)
			}
			log.Printf("tmux server gone (%v), exiting", err)
			h.closeAll()
			return
		}
		// Snapshot, not diff: after a reconnect gap, diffs against the old
		// world could be stale mid-gap; a snapshot is always truthful.
		d.gitScan(&w) // synchronous once: the first snapshot carries branches
		d.injectGit(&w)
		h.setWorld(w, nil, true, tmuxSock)
		d.armDetect(w)
		// Order matters: sweepOwned puts back everything a dead daemon's marks
		// describe, and it has to run before anything READS a status format —
		// statusRows assumes nobody has wrapped the session it is reading.
		d.sweepSpacers(ctl)
		d.sweepOwned(ctl)
		d.sweepLegacyState(ctl)
		d.sweepLegacyPad(ctl)
		if ln == nil {
			// Bind only now, with a populated world: a subscriber must never
			// see the socket before there is a truthful snapshot behind it
			// (an early `ls` would print an empty world and exit 0).
			if err := os.MkdirAll(filepath.Dir(winchSock), 0o700); err != nil {
				ctl.close()
				log.Fatalf("socket dir: %v", err)
			}
			ln, err = listenExclusive(winchSock)
			if err != nil {
				ctl.close()
				log.Fatalf("%v", err)
			}
			defer os.Remove(winchSock)
			go serve(ln, h, tmuxSock)
			log.Printf("attached to %s: %d sessions, %d windows, %d panes",
				tmuxSock, len(w.Sessions), len(w.Windows), len(w.Panes))
		} else {
			log.Printf("reattached to %s", tmuxSock)
		}

		if !consume(d, ctl, w, sig) {
			ctl.close()
			h.closeAll()
			return
		}
		ctl.close()
		// The attached session died (detach-on-destroy emits %exit while the
		// server lives on — verified). Give tmux a beat, then reattach.
		time.Sleep(100 * time.Millisecond)
	}
}

// consume runs the event loop for one control connection: re-lists, client
// commands, and sidebar upkeep all execute here, single-threaded — the
// serialization that makes the sh spike's race classes unrepresentable.
// Returns true if the connection ended but the daemon should reattach, false
// on shutdown.
func consume(d *daemon, ctl *control, w world, sig chan os.Signal) bool {
	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-ctl.kick:
			if fire == nil {
				timer = time.NewTimer(debounce)
				fire = timer.C
			}
		case <-fire:
			if len(d.h.cmds) > 0 || time.Since(d.lastScrub) < scrubQuiet {
				// A scrub is in flight or just landed: the world churn is our
				// own doing. Yield and re-arm — user input keeps full
				// priority, the re-list lands once scrubbing settles.
				timer = time.NewTimer(debounce)
				fire = timer.C
				continue
			}
			fire = nil
			start := time.Now()
			next, err := fetchWorld(ctl)
			if err != nil {
				// Connection is dying; the done case handles the rest.
				continue
			}
			d.checkSeen(&next) // client moved onto a done pane's window?
			d.injectAgents(&next)
			d.injectGit(&next)
			ops := diffWorlds(w, next)
			w = next
			d.h.setWorld(w, ops, false, d.tmuxSock)
			d.armDetect(w)
			d.pushStatusOpt(ctl, &w)
			d.checkDock(ctl, w)
			if dur := time.Since(start); dur > 25*time.Millisecond {
				log.Printf("relist took %s ops=%d", dur, len(ops))
			} else if bench {
				log.Printf("bench relist ops=%d dur_us=%d", len(ops), dur.Microseconds())
			}
		case env := <-d.h.cmds:
			d.handleCmd(ctl, env)
		case <-d.pv.tickC:
			// Live preview stream: nil (never fires) unless the billboards
			// are showing (a docked zoom-scrub). Yield to queued commands —
			// a mid-scrub tick would capture a target that's about to change
			// anyway.
			streaming := d.pv.target != "" && d.dock != nil && d.dock.scrubbing
			if streaming && len(d.h.cmds) == 0 {
				_ = d.preview(ctl, d.pv.target, false, true)
			}
		case <-d.det.tickC:
			// Agent detection pass. Yields to queued commands; a skipped
			// tick just means the next one classifies, 300ms later.
			if len(d.h.cmds) == 0 {
				d.detectTickRun(ctl, &w)
			}
		case <-d.gitC:
			// Slow git repoll (branch / ahead-behind per session). Only a
			// change publishes; the diff is a couple of session puts.
			if len(d.h.cmds) == 0 && d.gitScan(&w) {
				next := w
				next.Sessions = append([]session(nil), w.Sessions...)
				d.injectGit(&next)
				ops := diffWorlds(w, next)
				w = next
				if len(ops) > 0 {
					d.h.setWorld(w, ops, false, d.tmuxSock)
				}
			}
		case <-d.closeC:
			d.closeC = nil
			d.flushPendingClose(ctl)
		case <-d.focusC:
			d.focusC = nil
			if p := d.dock; p != nil && p.pane == d.focusPane &&
				p.win == p.originWin && !p.scrubbing {
				_, _ = ctl.run("select-pane -t " + q(p.pane))
			}
			d.focusPane = ""
		case <-d.releaseC:
			d.releaseC = nil
			if len(d.pendingRelease) == 0 {
				continue
			}
			if len(d.h.cmds) > 0 {
				// User input first; the stall can wait another tick.
				d.armRelease(releaseTick)
				continue
			}
			// A visibly spacer-held window releases now; otherwise the
			// stall waits for the user's typing to go quiet.
			pick, idle := d.releasePick(ctl)
			if pick < 0 && !idle {
				d.armRelease(releaseRetry)
				continue
			}
			if pick < 0 {
				pick = 0
			}
			it := d.pendingRelease[pick]
			d.pendingRelease = append(d.pendingRelease[:pick], d.pendingRelease[pick+1:]...)
			d.releaseOne(ctl, it)
			if len(d.pendingRelease) > 0 {
				d.armRelease(releaseTick)
			}
		case <-ctl.done:
			if timer != nil {
				timer.Stop()
			}
			return true
		case s := <-sig:
			log.Printf("%v, shutting down", s)
			return false
		}
	}
}

// listenExclusive binds the winch socket, stealing it only from the dead: if
// something answers on the socket, another daemon owns this server.
func listenExclusive(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err == nil {
		return ln, nil
	}
	conn, derr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if derr == nil {
		conn.Close()
		return nil, fmt.Errorf("daemon already running on %s", path)
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return nil, fmt.Errorf("stale socket %s: %v", path, rerr)
	}
	return net.Listen("unix", path)
}
