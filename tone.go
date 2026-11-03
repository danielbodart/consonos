package main

// Keeps the speaker awake. A Sonos on its optical input powers down after a
// while without a signal, and loses the start of whatever plays next while it
// wakes. A tone too low and too quiet to hear is still a signal on the line,
// so the line never goes quiet.

import (
	"fmt"
	"math"

	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

const toneRate = 48000

type Tone struct {
	client *pulse.Client
	stream *pulse.PlaybackStream
}

// PlayTone plays a sine of hz at amplitude (of full scale) to the named sink
// for as long as the process lives.
func PlayTone(sinkName string, hz, amplitude float64) (*Tone, error) {
	client, err := pulse.NewClient(
		pulse.ClientApplicationName("consonos"),
		pulse.ClientApplicationIconName("audio-speakers"),
	)
	if err != nil {
		return nil, err
	}
	sink, err := client.SinkByID(sinkName)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sink %s: %w", sinkName, err)
	}

	step := 2 * math.Pi * hz / toneRate
	phase := 0.0
	read := pulse.Float32Reader(func(buf []float32) (int, error) {
		// Interleaved stereo: the same sample on both channels.
		for i := 0; i+1 < len(buf); i += 2 {
			v := float32(amplitude * math.Sin(phase))
			buf[i], buf[i+1] = v, v
			phase = math.Mod(phase+step, 2*math.Pi)
		}
		return len(buf) &^ 1, nil
	})

	stream, err := client.NewPlayback(read,
		pulse.PlaybackSink(sink),
		pulse.PlaybackStereo,
		pulse.PlaybackSampleRate(toneRate),
		pulse.PlaybackLatency(0.5),
		pulse.PlaybackMediaName("Speaker keep-awake"),
		pulse.PlaybackRawOption(func(r *proto.CreatePlaybackStream) {
			// Stays on this sink whatever becomes the default, so the
			// session manager's follow-the-default never moves it.
			r.NoMove = true
			r.Properties["node.dont-move"] = proto.PropListString("true")
			r.Properties["node.name"] = proto.PropListString("consonos-keep-awake")
			// At unity, whatever volume a mixer remembers for streams.
			r.ChannelVolumes = proto.ChannelVolumes{proto.VolumeNorm, proto.VolumeNorm}
			r.VolumeSet = true
		}),
	)
	if err != nil {
		client.Close()
		return nil, err
	}
	stream.Start()
	return &Tone{client: client, stream: stream}, nil
}

// Err is the reason the tone stopped, once it has.
func (t *Tone) Err() error {
	if t.stream.Closed() {
		if err := t.stream.Error(); err != nil {
			return err
		}
		return fmt.Errorf("tone stream closed")
	}
	return nil
}
