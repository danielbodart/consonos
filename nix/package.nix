{
  lib,
  buildGo127Module,
  version,
  src,
}:

# The same static build as ./run, by nixpkgs' Go, and the check phase runs the
# tests on the way. buildGo127Module rather than buildGoModule, which is still
# Go 1.26 in the pinned nixpkgs and would refuse go.mod's `go 1.27`.
#
# Pure Go with CGO off: the PulseAudio protocol is spoken natively by
# jfreymuth/pulse rather than through libpulse, so the binary links nothing
# and needs nothing beside it at run time.
buildGo127Module {
  pname = "consonos";
  inherit version src;

  # jfreymuth/pulse, as `go mod vendor` lays it out. A go.sum change needs it
  # recomputed: build once with lib.fakeHash and take the hash Nix reports.
  vendorHash = "sha256-z2AMG6Ix1bakQOIvOX4njV7RpeBQR2qAfW8r4F7DHJI=";

  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];

  meta = {
    description = "A Sonos on the PC's optical output that follows the PC's volume, and never sleeps";
    homepage = "https://github.com/danielbodart/consonos";
    license = lib.licenses.mit;
    platforms = [
      "x86_64-linux"
      "aarch64-linux"
    ];
    mainProgram = "consonos";
  };
}
