//go:build !darwin

package main

import "fmt"

// Everywhere that is not macOS. There is nothing to do here, and that is the
// point: Linux and BSD desktops attribute a notification to whatever
// notify-send passes, and freedesktop has no equivalent of macOS's "the
// notification belongs to a registered app bundle" rule. The whole bundle
// apparatus — the Objective-C helper, the .icns, lsregister — exists to
// solve a problem these platforms do not have.
//
// Kept as explicit stubs rather than runtime.GOOS branches so a non-darwin
// build cannot compile a reference to a bundle path that will never exist.

// notifyAppCmd never applies off darwin; notify.go falls through to
// notify-send.
func notifyAppCmd(title, body, bundle, sock, pane string) (string, []string, bool) {
	return "", nil, false
}

func cmdNotifyInstall() {
	fmt.Println("notify-install is macOS only.")
	fmt.Println("Elsewhere winch notifies through your terminal (an OSC to the")
	fmt.Println("client tty, which follows you over ssh) or through notify-send;")
	fmt.Println("neither needs anything registered. See: winch notify-test")
}
