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
      packages = forAll (
        pkgs:
        let
          inherit (pkgs.stdenv.hostPlatform) isDarwin;

          # winch's own notification app. A macOS notification is attributed
          # to an APP, not a process: without a bundle the modern API refuses
          # us, osascript lends us Script Editor's identity (hence clicking a
          # notification opening Script Editor), and terminal-notifier lends
          # us its own AND has to be installed. This is the only route to
          # winch's name, icon and click behaviour with nothing to install.
          #
          # Darwin only, and consumed through winch's ldflags so the daemon
          # holds an absolute path rather than searching for it.
          winch-notify = pkgs.stdenv.mkDerivation {
            pname = "winch-notify";
            version = "0.4.0";
            src = self;

            # The icon is generated, not committed: a few rectangles from
            # image/png packed into .icns, which keeps the build free of both
            # ImageMagick and /usr/bin/iconutil (a system path a nix build has
            # no business assuming).
            nativeBuildInputs = [ pkgs.go ];

            buildPhase = ''
              runHook preBuild
              export HOME=$TMPDIR GOCACHE=$TMPDIR/go GOFLAGS=-mod=mod
              go run ./platform/darwin/mkicns winch.icns
              $CC -O2 -fobjc-arc -o winch-notify platform/darwin/notifier/main.m \
                -framework Foundation -framework AppKit -framework UserNotifications
              runHook postBuild
            '';

            installPhase = ''
              runHook preInstall
              app=$out/Applications/winch-notify.app
              mkdir -p $app/Contents/MacOS $app/Contents/Resources
              cp winch-notify $app/Contents/MacOS/winch-notify
              cp platform/darwin/notifier/Info.plist $app/Contents/Info.plist
              cp winch.icns $app/Contents/Resources/winch.icns

              # Ad-hoc, like every other unsigned nix-built app. Verified not
              # to be the blocker: terminal-notifier carries an identical
              # adhoc signature and registers fine — what matters is
              # lsregister, which `winch notify-install` does.
              /usr/bin/codesign --force --sign - --timestamp=none $app 2>&1 || \
                echo "codesign failed; the bundle may not register"

              mkdir -p $out/bin
              ln -s $app/Contents/MacOS/winch-notify $out/bin/winch-notify
              runHook postInstall
            '';

            dontStrip = true;
          };

          winch = pkgs.buildGoModule {
            pname = "winch";
            version = "0.4.0";

            src = self;
            subPackages = [ "cmd/winch" ];
            vendorHash = "sha256-W78PHNVSHhTrtZ6/7HfdmD+LjniySClfNbWpLaKTDRY=";

            ldflags = [
              "-X"
              "main.tmuxPath=${pkgs.tmux}/bin/tmux"
            ]
            ++ pkgs.lib.optionals isDarwin [
              "-X"
              "main.notifyApp=${winch-notify}/Applications/winch-notify.app"
            ];
          };
        in
        {
          inherit winch;
          default = winch;
        }
        // pkgs.lib.optionalAttrs isDarwin { inherit winch-notify; }
      );

      overlays.default = final: prev: {
        winch = self.packages.${prev.stdenv.hostPlatform.system}.winch;
      };

      # home-manager module. Installs winch and, on darwin, keeps its
      # notification bundle registered.
      #
      #   imports = [ inputs.winch.homeManagerModules.default ];
      #   programs.winch.enable = true;
      #
      # The registration is the whole reason this module exists rather than
      # just `home.packages = [ winch ]`. macOS delivers a notification on
      # behalf of an APP, and UNUserNotificationCenter only talks to apps
      # LaunchServices knows about — which it learns by scanning
      # /Applications and ~/Applications, never the nix store. So the bundle
      # needs an explicit lsregister, and since every rebuild moves its store
      # path, a registration made once is stranded by the next switch.
      # Notifications then fail SILENTLY: no error, nothing in System
      # Settings, no clue anywhere.
      #
      # Nobody should have to know that to get a working notification, which
      # is exactly why it belongs here and not in each user's config.
      homeManagerModules.default =
        {
          config,
          lib,
          pkgs,
          ...
        }:
        let
          cfg = config.programs.winch;
        in
        {
          options.programs.winch = {
            enable = lib.mkEnableOption "winch, a sidebar for tmux";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.winch;
              defaultText = lib.literalExpression "winch.packages.\${system}.winch";
              description = "The winch package to install.";
            };

            registerNotifier = lib.mkOption {
              type = lib.types.bool;
              default = pkgs.stdenv.hostPlatform.isDarwin;
              defaultText = lib.literalExpression "stdenv.hostPlatform.isDarwin";
              description = ''
                Re-register winch's notification bundle with LaunchServices on
                every activation (macOS only; a no-op elsewhere).

                Without this, desktop notifications on macOS fail silently
                after the first rebuild, because the registration points at a
                store path that has moved. You still have to enable winch once
                in System Settings > Notifications — every macOS app needs
                that, and nothing can do it for you.
              '';
            };
          };

          config = lib.mkIf cfg.enable {
            home.packages = [ cfg.package ];

            home.activation = lib.mkIf (cfg.registerNotifier && pkgs.stdenv.hostPlatform.isDarwin) {
              winchNotifyInstall = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
                run ${cfg.package}/bin/winch notify-install > /dev/null || \
                  echo "winch: notify-install failed; desktop notifications may not work"
              '';
            };
          };
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
