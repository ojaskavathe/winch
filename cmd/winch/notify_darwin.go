//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// macOS notification delivery through winch's OWN app bundle
// (platform/darwin/notifier). Everything here is darwin-only by construction
// rather than by a runtime.GOOS check, so a Linux build cannot accidentally
// reference a bundle path, an Objective-C helper, or lsregister.

// notifyApp is winch-notify.app, baked in by the flake's ldflags. Empty on a
// plain `go build`, in which case the terminal-notifier and osascript
// fallbacks in notify.go still apply.
var notifyApp = ""

// notifyAppCmd is the preferred macOS route: winch's own bundle, so the
// banner carries winch's name and icon rather than Script Editor's or
// terminal-notifier's, and clicking it can jump tmux to the pane.
//
// The pane, socket, winch path and terminal id ride in the notification's
// userInfo rather than in argv, because the click is delivered to a
// RELAUNCHED copy of the app which has neither argv nor any memory of the
// process that posted.
func notifyAppCmd(title, body, bundle, sock, pane string) (string, []string, bool) {
	if notifyApp == "" {
		return "", nil, false
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "winch"
	}
	args := []string{"--title", title, "--body", body, "--winch", exe}
	if sock != "" {
		args = append(args, "--socket", sock)
	}
	if pane != "" {
		args = append(args, "--pane", pane)
	}
	if bundle != "" {
		args = append(args, "--bundle", bundle)
	}
	return notifyApp + "/Contents/MacOS/winch-notify", args, true
}

// cmdNotifyInstall registers winch-notify.app with LaunchServices.
//
// This is the one step a nix install cannot skip, and the reason took an
// afternoon to find. UNUserNotificationCenter only talks to apps
// LaunchServices knows about, and it only knows about apps in the places it
// scans — /Applications, ~/Applications — never the nix store. Unregistered,
// requestAuthorization returns "Notifications are not allowed for this
// application", nothing appears in System Settings, and every notification
// fails silently.
//
// It is also the whole explanation for a puzzle that had nothing to do with
// code signing: kitty uses this API and never registers from the store,
// while terminal-notifier uses the DEPRECATED NSUserNotification API, which
// has no such requirement and works from anywhere. Identical ad-hoc
// signatures, opposite outcomes.
//
// Idempotent, so a home-manager activation script can just run it — and it
// must, because every rebuild moves the store path and strands the last one.
func cmdNotifyInstall() {
	if notifyApp == "" {
		fmt.Fprintln(os.Stderr, "this winch was built without winch-notify.app\n"+
			"(the flake adds it on darwin; a plain `go build` does not)")
		os.Exit(1)
	}
	if _, err := os.Stat(notifyApp); err != nil {
		fmt.Fprintf(os.Stderr, "winch-notify.app is missing at %s: %v\n", notifyApp, err)
		os.Exit(1)
	}
	const lsregister = "/System/Library/Frameworks/CoreServices.framework/" +
		"Frameworks/LaunchServices.framework/Support/lsregister"
	out, err := exec.Command(lsregister, "-f", notifyApp).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lsregister: %v\n%s", err, out)
		os.Exit(1)
	}
	fmt.Printf("registered %s\n\n", notifyApp)
	fmt.Println("Now enable it once: System Settings > Notifications > winch.")
	fmt.Println("Then check it works:  winch notify-test system")
}
