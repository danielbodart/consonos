# consonos as a home-manager user service.
#
# It belongs to a login session rather than the system: the sink it watches
# and the tone it plays both go through that user's sound server.
self:
{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.consonos;

  args = [
    "-sink"
    cfg.sink
    "-speaker"
    cfg.speaker
    # A Go boolean flag takes its value only with `=`; `-keep-awake false`
    # would be read as true followed by a stray argument.
    "-keep-awake=${lib.boolToString cfg.keepAwake.enable}"
    "-keep-awake-hz"
    (toString cfg.keepAwake.hz)
    "-keep-awake-level"
    (toString cfg.keepAwake.level)
  ];
in
{
  options.services.consonos = {
    enable = lib.mkEnableOption "consonos, which makes a Sonos on the optical output follow the PC's volume";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "consonos.packages.\${system}.default";
      description = "The consonos package to run.";
    };

    sink = lib.mkOption {
      type = lib.types.str;
      example = "alsa_output.pci-0000_00_1f.3.iec958-stereo";
      description = ''
        Name of the PulseAudio/PipeWire sink that feeds the speaker, as
        `pactl list short sinks` shows it.
      '';
    };

    speaker = lib.mkOption {
      type = lib.types.str;
      example = "sonos-arc.local";
      description = "Host name or address of the Sonos speaker.";
    };

    keepAwake = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Play an inaudible tone to the sink so the speaker never goes to
          sleep and never clips the start of whatever plays next.
        '';
      };

      hz = lib.mkOption {
        type = lib.types.number;
        default = 20;
        description = "Frequency of the keep-awake tone.";
      };

      level = lib.mkOption {
        type = lib.types.number;
        default = 0.001;
        description = "Amplitude of the keep-awake tone, as a fraction of full scale.";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    systemd.user.services.consonos = {
      Unit = {
        Description = "consonos: Sonos volume follows the PC's";
        Documentation = "https://github.com/danielbodart/consonos";
        # consonos lives and dies with the PulseAudio side of PipeWire: it
        # exits when that connection closes, since the sink it was watching
        # may be gone. BindsTo stops it when pipewire-pulse stops, and
        # WantedBy below starts it again when pipewire-pulse comes back --
        # pipewire-pulse is socket-activated, so it may start late or more
        # than once in a session.
        After = [ "pipewire-pulse.service" ];
        BindsTo = [ "pipewire-pulse.service" ];
      };

      Service = {
        Type = "simple";
        ExecStart = "${lib.getExe cfg.package} ${lib.escapeShellArgs args}";
        # For exits of its own: chiefly the sink not existing yet when it
        # starts, because WirePlumber has not created it. The speaker side
        # never ends the process; it reconnects internally.
        Restart = "always";
        RestartSec = 5;
      };

      # default.target as well, so it runs at login even before anything else
      # has connected to the pulse socket; starting it pulls pipewire-pulse in
      # through BindsTo.
      Install.WantedBy = [
        "default.target"
        "pipewire-pulse.service"
      ];
    };
  };
}
