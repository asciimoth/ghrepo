# Usage:
# - Build the package: nix build .#ghrepo
# - Run the CLI: nix run github:asciimoth/ghrepo#ghrepo -- --help
# - Add to a temporary shell: nix shell github:asciimoth/ghrepo#ghrepo
# - Add to a profile: nix profile install github:asciimoth/ghrepo#ghrepo
# - Install in a NixOS configuration:
#     environment.systemPackages = [ inputs.ghrepo.packages.${pkgs.system}.ghrepo ];
{
  description = "Tool for hosting deb/rm repos on GH pages";
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils = {
      url = "github:numtide/flake-utils";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    pre-commit-hooks = {
      url = "github:cachix/pre-commit-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };
  outputs = {
    self,
    nixpkgs,
    flake-utils,
    pre-commit-hooks,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };

      # mdformat 0.7.22 works with markdown-it-py 4, but its package metadata
      # still has an upper bound of version 3.
      markdownPython = pkgs.python3.override {
        packageOverrides = _: previous: {
          mdformat = previous.mdformat.overridePythonAttrs (_: {
            pythonRelaxDeps = [ "markdown-it-py" ];
          });
        };
      };
      markdownFormatter = markdownPython.withPackages (
        pythonPackages: with pythonPackages; [
          mdformat
          mdformat-gfm
          mdformat-gfm-alerts
        ]
      );

      ghrepo = pkgs.buildGoModule {
        pname = "ghrepo";
        version = "unstable";
        src = ./.;
        vendorHash = "sha256-eLxLRvCh2ZeokRpFSEa3fmcK6llE2zIQYv/J3JLqXFc=";
        nativeBuildInputs = [ pkgs.makeWrapper ];
        # GPG agent sockets inside test repositories break dry-run copy tests
        # in the Nix sandbox. Run the Go suite outside the package build.
        doCheck = false;
        postInstall = ''
          wrapProgram "$out/bin/ghrepo" \
            --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.git pkgs.gnupg ]}
        '';
      };

      checks = {
        pre-commit-check = pre-commit-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
            gotest.enable = true;
            commitizen.enable = true;
            typos.enable = true;
            typos-commit = {
              enable = true;
              description = "Find typos in commit message";
              entry = let script = pkgs.writeShellScript "typos-commit" ''
                typos "$1"
              ''; in builtins.toString script;
              stages = [ "commit-msg" ];
            };
            govet.enable = true;
            gofmt.enable = true;
            golangci-lint.enable = true;
            gotidy = {
              enable = true;
              description = "Makes sure go.mod matches the source code";
              entry = let script = pkgs.writeShellScript "gotidyhook" ''
                go mod tidy -v
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
          };
        };
      };
    in {
      packages = {
        inherit ghrepo;
        default = ghrepo;
      };

      devShells.default = pkgs.mkShell {
        inherit (checks.pre-commit-check) shellHook;
        buildInputs = with pkgs; [
          go
          golangci-lint
          gopls

          gh
          goreleaser
          gnupg
          python3

          typos
          commitizen
          just
          markdownFormatter
        ];
      };
    });
}
