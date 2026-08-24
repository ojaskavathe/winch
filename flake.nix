{
  description = "winch — a sidebar for tmux: sessions, agents, live previews";

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
        winch = pkgs.buildGoModule {
          pname = "winch";
          version = "0.4.0";

          src = self;
          subPackages = [ "cmd/winch" ];
          vendorHash = "sha256-W78PHNVSHhTrtZ6/7HfdmD+LjniySClfNbWpLaKTDRY=";

          ldflags = [
            "-X"
            "main.tmuxPath=${pkgs.tmux}/bin/tmux"
          ];
        };

        default = winch;
      });

      overlays.default = final: prev: {
        winch = self.packages.${prev.stdenv.hostPlatform.system}.winch;
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
