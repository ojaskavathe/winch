package main

import "testing"

// The check is named for the BIND, and for a long time it read
// os.Executable() instead — so running doctor as the installed binary
// compared the profile to itself and passed while tmux still held the
// previous generation. That is the normal state after every rebuild,
// because tmux never re-reads its config.
func TestStorePrefixIdentifiesTheBuild(t *testing.T) {
	for in, want := range map[string]string{
		"/nix/store/abc123-winch-0.4.0/bin/winch":                      "/nix/store/abc123-winch-0.4.0",
		"/nix/store/abc123-winch-0.4.0/bin/winch\"":                    "/nix/store/abc123-winch-0.4.0",
		`/nix/store/xyz-winch-0.4.0/bin/winch toggle "#{client_name}"`: "/nix/store/xyz-winch-0.4.0",
		"/nix/store/only": "/nix/store/only",
	} {
		if got := storePrefix(in); got != want {
			t.Errorf("storePrefix(%q) = %q want %q", in, got, want)
		}
	}
	// Two generations of the same package must not compare equal.
	a := storePrefix("/nix/store/69ss8rxzc3m2wrazwkmb503mdcf9kzlp-winch-0.4.0/bin/winch")
	b := storePrefix("/nix/store/b8nrzgbdb562hqgghjnnr5w90ji638ya-winch-0.4.0/bin/winch")
	if a == b {
		t.Fatalf("distinct builds compared equal: %s", a)
	}
}
