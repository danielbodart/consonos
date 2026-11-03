# The home-manager module, evaluated against just the options it sets, so the
# check needs no home-manager input: the service runs this flake's binary
# with the sink and speaker it was given, a disabled keep-awake reaches the
# binary as one argument Go reads as false, it comes and goes with the sound
# server, and nothing is set when it is off.
{
  pkgs,
  lib,
  self,
}:

let
  stub = {
    options = {
      home.packages = lib.mkOption {
        type = lib.types.listOf lib.types.package;
        default = [ ];
      };
      systemd.user.services = lib.mkOption {
        type = lib.types.attrsOf lib.types.anything;
        default = { };
      };
    };
  };
  eval =
    settings:
    (lib.evalModules {
      modules = [
        stub
        self.homeModules.default
        {
          _module.args.pkgs = pkgs;
          services.consonos = settings;
        }
      ];
    }).config;

  sink = "alsa_output.pci-0000_00_1f.3.iec958-stereo";
  on = eval {
    enable = true;
    inherit sink;
    speaker = "office-sonos.lan";
  };
  quiet = eval {
    enable = true;
    inherit sink;
    speaker = "office-sonos.lan";
    keepAwake.enable = false;
  };
  off = eval { };

  package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
  unit = on.systemd.user.services.consonos;
  exec = unit.Service.ExecStart;
in
assert lib.assertMsg (lib.hasPrefix "${package}/bin/consonos -sink ${sink} -speaker office-sonos.lan '-keep-awake=true' " exec) "service runs ${exec}";
assert lib.assertMsg (lib.hasInfix " '-keep-awake=false' " quiet.systemd.user.services.consonos.Service.ExecStart) "keep-awake off runs ${quiet.systemd.user.services.consonos.Service.ExecStart}";
assert lib.assertMsg (unit.Unit.BindsTo == [ "pipewire-pulse.service" ]) "not bound to pipewire-pulse";
assert lib.assertMsg (unit.Service.Restart == "always") "not restarted";
assert lib.assertMsg (on.home.packages == [ package ]) "packages ${toString on.home.packages}";
assert lib.assertMsg (off.systemd.user.services == { } && off.home.packages == [ ]) "disabled but configured";
pkgs.writeText "consonos-home-eval" exec
