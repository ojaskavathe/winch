{
  description = "demux — a sidebar for tmux: sessions, agents, live previews";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: rec {
        demuxd = pkgs.buildGoModule {
          pname = "demuxd";
          version = "0.4.0";

          src = self;
          subPackages = [ "cmd/demuxd" ];
          vendorHash = "sha256-W78PHNVSHhTrtZ6/7HfdmD+LjniySClfNbWpLaKTDRY=";

          ldflags = [
            "-X"
            "main.tmuxPath=${pkgs.tmux}/bin/tmux"
          ];
        };

        # sidebar launcher: docks/undocks the list pane via demuxd
        demux = pkgs.writeShellApplication {
          name = "demux";
          runtimeInputs = [ pkgs.tmux ];
          text = builtins.replaceStrings [ "@frameconf@" ] [ "${./frame.conf}" ] (
            builtins.readFile ./sidebar.sh
          );
        };

        default = demuxd;
      });

      overlays.default = final: prev: {
        demuxd = self.packages.${prev.stdenv.hostPlatform.system}.demuxd;
        demux = self.packages.${prev.stdenv.hostPlatform.system}.demux;
      };

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.tmux
          ];
        };
      });
    };
}
