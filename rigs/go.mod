// Integration rigs for winch. A separate module on purpose: winch's own
// build/vet/test (and its nix checkPhase) must never pull these in — they
// need a live tmux server and real ptys, which only exist on a dev machine.
module winchrigs

go 1.22
