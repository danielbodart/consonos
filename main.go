// consonos -- cōnsonō, Latin: to sound together, to be in harmony.
//
// Makes a Sonos on a PC's optical output behave like the PC's own speakers:
// the sink's volume and mute become the speaker's, both ways, and a tone
// below hearing keeps the speaker from sleeping.
package main

import (
	"crypto/rsa"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"
)

var version = "development"

func main() {
	sinkName := flag.String("sink", "", "PulseAudio/PipeWire sink feeding the speaker, e.g. alsa_output.pci-0000_00_1f.3.iec958-stereo")
	host := flag.String("speaker", "", "the speaker's host name or address")
	tone := flag.Bool("keep-awake", true, "play an inaudible tone to the sink so the speaker never sleeps")
	toneHz := flag.Float64("keep-awake-hz", 20, "keep-awake tone frequency")
	toneLevel := flag.Float64("keep-awake-level", 0.001, "keep-awake tone amplitude, of full scale")
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

	rootPEM := sonosRootPEM
	if *rootPath != "" {
		var err error
		if rootPEM, err = os.ReadFile(*rootPath); err != nil {
			log.Error("sonos-root", "err", err)
			os.Exit(2)
		}
	}
	root, err := ParseRootKey(rootPEM)
	if err != nil {
		log.Error("sonos-root", "err", err)
		os.Exit(2)
	}

	if err := run(*sinkName, *host, root, *tone, *toneHz, *toneLevel, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

// run returns only when the sound server goes, which is fatal: the service
// manager restarts us alongside it. The speaker, which comes and goes with
// the network, is reconnected to without ending anything.
func run(sinkName, host string, root *rsa.PublicKey, tone bool, toneHz, toneLevel float64, log *slog.Logger) error {
	sink, err := OpenSink(sinkName)
	if err != nil {
		return err
	}

	var t *Tone
	if tone {
		if t, err = PlayTone(sinkName, toneHz, toneLevel); err != nil {
			return fmt.Errorf("keep-awake: %w", err)
		}
	}

	speaker := NewSpeaker(host, root, log)
	fromSpeaker := make(chan Level, 16)
	go speaker.Run(func(l Level) { fromSpeaker <- l })

	bridge := NewBridge(sink, speaker, func(msg string, args ...any) { log.Warn(msg, args...) })

	readSink := func() error {
		l, err := sink.Level()
		if err != nil {
			return err
		}
		bridge.FromSink(l, time.Now())
		return nil
	}
	if err := readSink(); err != nil {
		return err
	}
	log.Info("started", "sink", sinkName, "speaker", host, "keep-awake", tone)

	health := time.NewTicker(5 * time.Second)
	defer health.Stop()
	for {
		select {
		case <-sink.closed:
			return fmt.Errorf("sound server connection closed")
		case <-sink.changed:
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
