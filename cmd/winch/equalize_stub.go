//go:build noequalize

package main

import (
	"fmt"
	"os"
)

// The equalize subcommand is compile-time optional: `-tags noequalize`
// drops equalize.go and the nvim RPC client from the binary entirely.
// Everything else in winch is independent of them.
func cmdEqualize(_, _ string) {
	fmt.Fprintln(os.Stderr, "winch: built without equalize (-tags noequalize)")
	os.Exit(1)
}

// dockEqualize is a no-op without equalize compiled in; the daemon still
// answers the command so the bind's run-shell does not error.
func (d *daemon) dockEqualize(_ *control) error { return nil }
