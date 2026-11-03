<p align="center"><img src="logo.png" alt="consonos" width="600"></p>

# consonos

A Sonos speaker on a PC's optical (S/PDIF) output, made to behave like the PC's own speakers. Turn the volume up in GNOME, press the volume keys, or mute, and the speaker follows; turn it on the speaker or in the Sonos app, and the PC's slider follows. And the speaker never falls asleep and swallows the first second of the next sound.

A small headless daemon: one static Go binary, no ports opened, nothing to configure beyond which sink and which speaker.

> *Cōnsōnō* is Latin: to sound together, to be in harmony. The PC and the
> speaker, at one volume.

## Why

S/PDIF carries audio and nothing else — no volume, no mute, no "wake up". So with a Sonos (an Arc, a Beam, a Ray) on the optical input, the PC's volume control does the wrong thing: it scales the digital signal, throwing away resolution before the speaker ever sees it, while the speaker's own volume sits wherever it was last left. Two volume controls, fighting. And after a few minutes of silence the speaker goes to sleep, and clips the start of whatever plays next while it wakes.

consonos makes the speaker's volume *the* volume, and keeps the line from ever going quiet.

## How it works

- **The PC side** is a PulseAudio or PipeWire sink, watched over the PulseAudio protocol (PipeWire serves it through `pipewire-pulse`). That is where GNOME's slider, the volume keys and anything else that sets volume all land, so watching the sink sees every one of them.
- **The speaker side** is the Sonos local control API: a WebSocket at `wss://<speaker>:1443/websocket/api`. consonos subscribes to the speaker's volume, so the speaker pushes every change — whoever made it — down the same connection consonos sends its own changes on. Outbound only: nothing listens on the PC.
- **The two are kept equal.** Whichever side last changed is the one the other follows. Volume maps 1:1: 35% in GNOME is 35 on the speaker. Each side echoes what it is told, so a report matching a value recently sent is taken as the echo and not passed back, which is what lets a slider be dragged quickly without being dragged back.
- **On connecting and reconnecting, the speaker wins.** It is the thing in the room, and it may have been turned while consonos was not watching. The speaker reconnects on its own with backoff; consonos exits only if the sound server goes, and the service manager restarts it.
- **Keep-awake**: a 20 Hz sine at 0.001 of full scale (−60 dBFS) is played to the sink for as long as consonos runs. Too low and too quiet to hear, but a signal on the line, so the speaker never sees silence. The stream is pinned to the sink, so it is not moved when the default output changes.

## Install

As a flake, with the home-manager module:

```nix
inputs.consonos = {
  url = "github:danielbodart/consonos";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

```nix
imports = [ inputs.consonos.homeModules.default ];

services.consonos = {
  enable = true;
  sink = "alsa_output.pci-0000_00_1f.3.iec958-stereo";
  speaker = "sonos-arc.local";
  # keepAwake = { enable = true; hz = 20; level = 0.001; };
};
```

That runs it as a systemd user service bound to `pipewire-pulse.service`. Use `git+https://github.com/danielbodart/consonos` as the URL if you want `consonos --version` to include the commit count.

Without Nix, every green build of trunk is a [release](https://github.com/danielbodart/consonos/releases/latest): a static binary for x64 and arm64, and a systemd user unit.

```sh
base=https://github.com/danielbodart/consonos/releases/latest/download
install -D -m 755 <(curl -fsSL $base/consonos-linux-x64) ~/.local/bin/consonos
curl -fsSL --create-dirs -o ~/.config/systemd/user/consonos.service $base/consonos.service
printf 'CONSONOS_SINK=%s\nCONSONOS_SPEAKER=%s\n' alsa_output.pci-0000_00_1f.3.iec958-stereo office-sonos.lan > ~/.config/consonos.env
systemctl --user enable --now consonos
```

Find the sink's name with `pactl list short sinks`. For the speaker, its IP address or its mDNS name (`avahi-browse -rt _sonos._tcp` lists them).

### Lock the sink at full scale

Without this, the PC still scales the signal down *and* the speaker turns down too: the volume is applied twice. PipeWire's `channelmix.lock-volumes` holds the sink at full scale on the wire while it still reports, and still accepts, whatever volume it is set to — and that reported volume is what consonos mirrors. A WirePlumber rule, in `~/.config/wireplumber/wireplumber.conf.d/51-consonos.conf`:

```
monitor.alsa.rules = [
  {
    matches = [ { node.name = "alsa_output.pci-0000_00_1f.3.iec958-stereo" } ]
    actions = { update-props = { channelmix.lock-volumes = true } }
  }
]
```

or from home-manager:

```nix
xdg.configFile."wireplumber/wireplumber.conf.d/51-consonos.conf".text = ''
  monitor.alsa.rules = [
    {
      matches = [ { node.name = "alsa_output.pci-0000_00_1f.3.iec958-stereo" } ]
      actions = { update-props = { channelmix.lock-volumes = true } }
    }
  ]
'';
```

then `systemctl --user restart wireplumber`.

## Flags

| Flag | Default | |
|---|---|---|
| `-sink` | *(required)* | The sink feeding the speaker |
| `-speaker` | *(required)* | The speaker's host name or address |
| `-keep-awake` | `true` | Play the inaudible tone; `-keep-awake=false` to turn it off |
| `-keep-awake-hz` | `20` | Its frequency |
| `-keep-awake-level` | `0.001` | Its amplitude, as a fraction of full scale |
| `-sonos-root` | *(built in)* | A PEM public key to verify the speaker's certificate with, in place of the Sonos device root |
| `--version` | | Print the version and exit |

Logs go to stderr without timestamps, for journald: `journalctl --user -u consonos`.

## Caveats

- **The Sonos root key was recovered, not published.** A speaker sends only its own certificate, and Sonos does not publish the "Sonos Device Authentication Root CA" that signs it. consonos verifies each speaker's certificate signature against that root's public key, which was worked out from the signatures of four speakers' certificates; see [docs/sonos-root.md](docs/sonos-root.md). It also checks that the certificate is in date, that it names the device the speaker says it is, and that it is the same device on every reconnect. `-sonos-root` takes a different key, should Sonos ever change theirs.
- **The API key is the public one.** The local API wants an `X-Sonos-Api-Key`; consonos sends the one Home Assistant's [sonos-websocket](https://github.com/jjlawren/sonos-websocket) uses. It identifies the kind of client rather than authorising anything.
- **One sink, one speaker.** Grouped speakers are not handled; the volume is the player's own, not the group's.

## Prior art

[speaker-volume-bridge](https://github.com/MiguelTVMS/speaker-volume-bridge) does the same job as a Rust/Tauri tray app, over UPnP/GENA and `pactl`. consonos is the headless take: a user service, the newer WebSocket API, S/PDIF locked at 100% volume (no signal degrade) and the keep-awake tone.

## Licence

MIT — see [LICENSE](LICENSE).
