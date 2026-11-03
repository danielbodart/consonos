{
  description = "consonos: a Sonos on the PC's optical output that follows the PC's volume";

  # Unstable for buildGo127Module, the Go that go.mod asks for and mise.toml
  # pins - the stable branch has no Go 1.27 yet.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      inherit (nixpkgs) lib;
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      # The same scheme as ./run: the committed major, the commit count, and
      # the commit's date in place of a CI run number. A dirty tree has no
      # count, and says 0.
      major = lib.fileContents ./MAJOR;
      version = "${major}.${toString (self.revCount or 0)}.${self.lastModifiedDate or "0"}";

      src = lib.fileset.toSource {
        root = ./.;
        # go.mod, go.sum and every .go file, tests included; the root key the
        # binary embeds; and the real speakers' certificates the tests verify
        # with it. So editing the docs or the flake does not rebuild it.
        fileset = lib.fileset.unions [
          ./go.mod
          ./go.sum
          (lib.fileset.fileFilter (file: file.hasExt "go") ./.)
          ./internal/sonos/root.pem
          ./internal/sonos/testdata
        ];
      };
    in
    {
      packages = forAllSystems (pkgs: {
        default = pkgs.callPackage ./nix/package.nix { inherit version src; };
      });

      # services.consonos, a user service, with this flake's package unless
      # one is given.
      homeModules.default = import ./nix/home.nix self;

      checks = forAllSystems (pkgs: {
        # Its check phase is `go test`, so building it runs the tests.
        package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        home = import ./nix/home-eval.nix { inherit pkgs lib self; };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
