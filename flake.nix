{
  description = "consonos: a Sonos on the PC's optical output that follows the PC's volume";

  # nixos-26.05, the release nix-config builds every host from. The package
  # needs little from it (a derivation that installs one file), and nothing
  # here builds Go any more, so the unstable branch's Go 1.27 is not needed.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      inherit (nixpkgs) lib;
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      # THE PACKAGE IS THE RELEASE BINARY, not a build from source (Dan,
      # 2026-11-15): the binaries are built once, by mise's Go in CI (./run
      # release), tested and published, and building them again here only
      # made a second binary that was not the one released. So a release's
      # tag is a commit of its own, a child of the released trunk commit,
      # that adds nix/release.json: the version, and for each system the
      # asset's URL and hash (scripts/release-json.sh writes it; ci.yml's
      # release job commits and tags it). A flake fetched as
      # github:danielbodart/consonos/v<V> cannot see its tag, only its
      # commit, which is why the version is in the file.
      #
      # On trunk there is no such file and no package: pin a release tag.
      # Absent rather than an error, so `nix flake check` still works on
      # trunk.
      releaseFile = ./nix/release.json;
      release = if builtins.pathExists releaseFile then lib.importJSON releaseFile else null;

      # One static binary (CGO off: the PulseAudio protocol is spoken
      # natively by jfreymuth/pulse, not through libpulse) installed as
      # bin/consonos, byte for byte the release's: no stripping or patching,
      # which a static Go binary does not need and which would make it
      # differ from the release's SHA256SUMS.
      mkConsonos =
        pkgs:
        { version, binary }:
        pkgs.stdenvNoCC.mkDerivation {
          pname = "consonos";
          inherit version;
          src = binary;
          dontUnpack = true;
          dontConfigure = true;
          dontBuild = true;
          installPhase = ''
            runHook preInstall
            install -Dm0755 $src $out/bin/consonos
            runHook postInstall
          '';
          dontFixup = true;
          meta = {
            description = "A Sonos on the PC's optical output that follows the PC's volume, and never sleeps";
            homepage = "https://github.com/danielbodart/consonos";
            license = lib.licenses.mit;
            mainProgram = "consonos";
            platforms = if release == null then [ ] else builtins.attrNames release.assets;
            sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
          };
        };

      # The release's package for each system it has an asset for.
      releasePackages = lib.mapAttrs (
        system: asset:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = mkConsonos pkgs {
            inherit (release) version;
            binary = pkgs.fetchurl { inherit (asset) url hash; };
          };
        }
      ) release.assets;
    in
    {
      packages = if release == null then { } else releasePackages;

      # services.consonos, a user service, with this flake's package unless
      # one is given (so, from a release tag).
      homeModules.default = import ./nix/home.nix self;

      # They test the release package, so they exist where nix/release.json
      # does: on a release tag, and in CI's nix job, which writes one for the
      # binaries its own run built (scripts/nix-local-release.sh). The Go
      # tests are ./run test's.
      checks = forAllSystems (
        pkgs:
        let
          system = pkgs.stdenv.hostPlatform.system;
          package = self.packages.${system}.default;
        in
        lib.optionalAttrs (self.packages ? ${system}) {
          inherit package;
          # The installed binary says the version release.json gives it.
          version = pkgs.runCommand "consonos-version" { } ''
            got=$(${lib.getExe package} --version)
            [ "$got" = ${lib.escapeShellArg release.version} ] || { echo "the binary says $got, the release ${release.version}"; exit 1; }
            touch $out
          '';
          home = import ./nix/home-eval.nix { inherit pkgs lib self; };
        }
      );

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
