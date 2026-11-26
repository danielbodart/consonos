// consonos -- cōnsonō, Latin: to sound together, to be in harmony.
//
// Makes a Sonos on a PC's optical output behave like the PC's own speakers:
// the sink's volume and mute become the speaker's, both ways, a tone below
// hearing keeps the speaker from sleeping, and using the sink puts the
// speaker back on its TV input.
package main

import (
	"crypto/rsa"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/danielbodart/consonos/internal/bridge"
	"github.com/danielbodart/consonos/internal/pulse"
	"github.com/danielbodart/consonos/internal/sonos"
)

var version = "development"

func main() {
	sinkName := flag.String("sink", "", "PulseAudio/PipeWire sink feeding the speaker, e.g. alsa_output.pci-0000_00_1f.3.iec958-stereo")
	host := flag.String("speaker", "", "the speaker's host name or address")
	tone := flag.Bool("keep-awake", true, "play an inaudible tone to the sink so the speaker never sleeps")
	toneHz := flag.Float64("keep-awake-hz", 20, "keep-awake tone frequency")
	toneLevel := flag.Float64("keep-awake-level", 0.001, "keep-awake tone amplitude, of full scale")
	switchInput := flag.Bool("switch-input", true, "put the speaker back on its TV input when the sink's volume or mute is changed on the PC, or the sink becomes the default output")
	rootPath := flag.String("sonos-root", "", "PEM public key to verify the speaker's certificate with, instead of the built-in Sonos device root")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *sinkName == "" || *host == "" {
		fmt.Fprintln(os.Stderr, "consonos: -sink and -speaker are required")
		flag.Usage()
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey { // journald stamps it
				return slog.Attr{}
			}
			return a
		},
	}))

	rootPEM := sonos.RootPEM
	if *rootPath != "" {
		var err error
		if rootPEM, err = os.ReadFile(*rootPath); err != nil {
			log.Error("sonos-root", "err", err)
			os.Exit(2)
		}
	}
	root, err := sonos.ParseRootKey(rootPEM)
	if err != nil {
		log.Error("sonos-root", "err", err)
		os.Exit(2)
	}

	if err := run(*sinkName, *host, root, *tone, *toneHz, *toneLevel, *switchInput, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

// run returns only when the sound server goes, which is fatal: the service
// manager restarts us alongside it. The speaker, which comes and goes with
// the network, is reconnected to without ending anything.
//
// Changing the sink's volume or mute on the PC, or making the sink the
// default output, is someone choosing to listen to the PC through the
// speaker. So each one, with switchInput, puts the speaker back on its TV
// input if something else had taken it. The sink following the speaker does
// not count, so turning the speaker while it plays from the app leaves it be.
func run(sinkName, host string, root *rsa.PublicKey, tone bool, toneHz, toneLevel float64, switchInput bool, log *slog.Logger) error {
	sink, err := pulse.OpenSink(sinkName)
	if err != nil {
		return err
	}

	var t *pulse.Tone
	if tone {
		if t, err = pulse.PlayTone(sinkName, toneHz, toneLevel); err != nil {
			return fmt.Errorf("keep-awake: %w", err)
		}
	}

	speaker := sonos.NewSpeaker(host, root, log)
	fromSpeaker := make(chan bridge.Level, 16)
	go speaker.Run(func(l bridge.Level) { fromSpeaker <- l })

	bridge := bridge.NewBridge(sink, speaker, func(msg string, args ...any) { log.Warn(msg, args...) })

	isDefault, err := sink.IsDefault()
	if err != nil {
		return err
	}
	readSink := func() error {
		l, err := sink.Level()
		if err != nil {
			return err
		}
		changed := bridge.FromSink(l, time.Now())
		nowDefault, err := sink.IsDefault()
		if err != nil {
			return err
		}
		becameDefault := nowDefault && !isDefault
		isDefault = nowDefault
		if switchInput && (changed || becameDefault) {
			speaker.WantTVInput()
		}
		return nil
	}
	if err := readSink(); err != nil {
		return err
	}
	log.Info("started", "sink", sinkName, "speaker", host, "keep-awake", tone, "switch-input", switchInput)

	health := time.NewTicker(5 * time.Second)
	defer health.Stop()
	for {
		select {
		case <-sink.Closed():
			return fmt.Errorf("sound server connection closed")
		case <-sink.Changed():
			if err := readSink(); err != nil {
				return err
			}
		case l := <-fromSpeaker:
			bridge.FromSpeaker(l, time.Now())
		case <-health.C:
			if t != nil {
				if err := t.Err(); err != nil {
					return fmt.Errorf("keep-awake: %w", err)
				}
			}
		}
	}
}
