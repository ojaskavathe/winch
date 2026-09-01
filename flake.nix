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

          # withNotifier is a real build switch, not just config. Referencing
          # winch-notify from the ldflags makes it a build AND runtime
          # dependency, so a darwin `winch` cannot otherwise be built without
          # an Objective-C toolchain and a codesign step, and the .app lands
          # in the closure of anyone who only wanted the tmux daemon.
          #
          # On by default because notifications are a headline feature and a
          # silent absence is the worst way to discover a missing one. Off is
          # one line:
          #
          #   winch.override { withNotifier = false; }
          #
          # nix's laziness is what makes this work: with the flag false the
          # ldflags never reference winch-notify, so it is never built.
          # withTmux pins the tmux BINARY winch runs. Off by default, and the
          # default is the correctness argument rather than the size one.
          #
          # winch never starts a server; it attaches to one somebody else
          # started (control.go, equalize.go). tmux checks PROTOCOL_VERSION on
          # connect and a client that disagrees is refused outright —
          # "protocol version mismatch (client N, server M)", exit 1
          # (client.c:670). So the binary has to match the SERVER's version,
          # which is a property of the user's tmux, not of winch's build day.
          # Pinning guarantees exactly the wrong one the moment winch's
          # nixpkgs drifts from theirs; it works today only because both
          # resolve to the same tmux-3.7b. Unpinned, tmuxPath falls back to
          # "tmux" on PATH — and winch is invariably invoked from a tmux
          # bind, so that is the very binary running the server.
          #
          # It also drops tmux, ncurses and libevent (~6MB) from the closure,
          # which is most of it.
          #
          # Turn it on where winch's PATH will not have tmux — launchd, or a
          # GUI-spawned process. It does NOT make winch self-contained: a
          # server still has to come from somewhere, since winch only ever
          # attaches to one.
          #
          #   winch.override { withTmux = true; }
          mkWinch = pkgs.lib.makeOverridable (
            {
              withNotifier ? isDarwin,
              withTmux ? false,
            }:
            pkgs.buildGoModule {
              pname = "winch";
              version = "0.4.0";

              src = self;
              subPackages = [ "cmd/winch" ];
              vendorHash = "sha256-W78PHNVSHhTrtZ6/7HfdmD+LjniySClfNbWpLaKTDRY=";

              ldflags = pkgs.lib.optionals withTmux [
                "-X"
                "main.tmuxPath=${pkgs.tmux}/bin/tmux"
              ]
              ++ pkgs.lib.optionals (withNotifier && isDarwin) [
                "-X"
                "main.notifyApp=${winch-notify}/Applications/winch-notify.app"
              ];
            }
          );

          winch = mkWinch { };
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

            notifications = lib.mkOption {
              type = lib.types.bool;
              default = pkgs.stdenv.hostPlatform.isDarwin;
              defaultText = lib.literalExpression "stdenv.hostPlatform.isDarwin";
              description = ''
                Build winch with its macOS notification bundle and keep it
                registered with LaunchServices.

                This is a BUILD switch, not just configuration: the bundle is
                referenced from winch's ldflags, so with it off the
                Objective-C helper is never compiled and never enters the
                closure. Someone who wants only the tmux daemon should not
                have to build a GUI app to get it.

                On elsewhere than macOS it does nothing — Linux and BSD
                notify through the terminal or notify-send, neither of which
                needs anything registered.

                You still have to enable winch once in System Settings >
                Notifications. Every macOS app needs that and nothing can
                consent on your behalf.
              '';
            };

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.winch.override {
                withNotifier = cfg.notifications;
              };
              defaultText = lib.literalExpression ''
                winch.packages.''${system}.winch.override { inherit withNotifier; }'';
              description = "The winch package to install.";
            };
          };

          config = lib.mkIf cfg.enable {
            home.packages = [ cfg.package ];

            home.activation = lib.mkIf (cfg.notifications && pkgs.stdenv.hostPlatform.isDarwin) {
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
