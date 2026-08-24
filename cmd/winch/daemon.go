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

	// statusBase caches each session's pre-pad status-format, keyed by
	// session id. Reading one costs several control-mode round trips, and a
	// cross-session hand-off is the most latency-critical path there is —
	// stalling it before the critical batch shows the user a blank sidebar
	// strip for a frame. Nobody edits their status format mid-scrub, and an
	// undock drops the whole cache, so a toggle picks up a changed config.
	statusBase map[string]statusSave

	// agentCycle: per-client position in the agent switcher (M-a). Rapid
	// re-invocations cycle down the attention-sorted list; after the tap
	// window it restarts at the top-attention agent.
	agentCycle map[string]agentCyclePos

	// git: per-session repo identity cache; gitC ticks the slow repoll
	// (git.go). The ticker lives for the daemon's lifetime, across
	// control-mode reconnects.
	git  map[string]gitInfo
	gitC <-chan time.Time

	// dockW: user-tuned sidebar width (0 = listWidth default). Survives
	// undocks; runtime-only for now.
	dockW int
}

type agentCyclePos struct {
	pane string
	at   time.Time
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

// uiBorderLines is tmux's own pane-border-lines, read at attach so the status
// pad can end in the same glyph tmux draws down the sidebar's border column.
// Same lifetime as uiTheme: changing it mid-session needs a daemon restart.
var uiBorderLines string

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
	d := &daemon{tmuxSock: tmuxSock, h: h}
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
		d.sweepSpacers(ctl)
		d.sweepDockedState(ctl)
		d.sweepStatusFormat(ctl)
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
			it := d.pendingRelease[0]
			d.pendingRelease = d.pendingRelease[1:]
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
