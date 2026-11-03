package main

import (
	"testing"
	"time"
)

type fakeSide struct {
	level Level
	sets  []Level
}

func (f *fakeSide) SetVolume(v int) error {
	f.level.Volume = v
	f.sets = append(f.sets, f.level)
	return nil
}
func (f *fakeSide) SetMute(m bool) error {
	f.level.Muted = m
	f.sets = append(f.sets, f.level)
	return nil
}

func newTestBridge() (*Bridge, *fakeSide, *fakeSide) {
	sink, speaker := &fakeSide{}, &fakeSide{}
	return NewBridge(sink, speaker, func(string, ...any) {}), sink, speaker
}

var t0 = time.Unix(1_800_000_000, 0)

func TestSpeakerWinsOnConnect(t *testing.T) {
	b, sink, speaker := newTestBridge()
	b.FromSink(Level{Volume: 80}, t0)
	if len(speaker.sets) != 0 {
		t.Fatalf("sink pushed to an unheard speaker: %v", speaker.sets)
	}
	b.FromSpeaker(Level{Volume: 26}, t0)
	if sink.level != (Level{Volume: 26}) {
		t.Fatalf("sink = %v, want 26", sink.level)
	}
	if len(speaker.sets) != 0 {
		t.Fatalf("speaker was set: %v", speaker.sets)
	}
}

func TestSinkChangeReachesSpeakerOnce(t *testing.T) {
	b, sink, speaker := newTestBridge()
	b.FromSpeaker(Level{Volume: 30}, t0)
	b.FromSink(Level{Volume: 30}, t0) // the echo of following the speaker
	b.FromSink(Level{Volume: 31}, t0)
	b.FromSpeaker(Level{Volume: 31}, t0) // its echo
	if len(speaker.sets) != 1 || speaker.level.Volume != 31 {
		t.Fatalf("speaker sets = %v", speaker.sets)
	}
	if len(sink.sets) != 0 { // the sink was unheard when the speaker spoke
		t.Fatalf("sink sets = %v", sink.sets)
	}
}

// A slider dragged quickly: the speaker's late echoes of earlier values must
// not drag the sink back.
func TestLateEchoesDoNotFightADrag(t *testing.T) {
	b, sink, speaker := newTestBridge()
	b.FromSpeaker(Level{Volume: 30}, t0)
	b.FromSink(Level{Volume: 30}, t0)
	for v := 31; v <= 35; v++ {
		b.FromSink(Level{Volume: v}, t0)
	}
	for v := 31; v <= 35; v++ {
		b.FromSpeaker(Level{Volume: v}, t0.Add(100*time.Millisecond))
	}
	if len(sink.sets) != 0 {
		t.Fatalf("sink was dragged back: %v", sink.sets)
	}
	if speaker.level.Volume != 35 {
		t.Fatalf("speaker = %d", speaker.level.Volume)
	}
}

// The same, turning the speaker: the sink's late echoes must not drag it back.
func TestLateEchoesDoNotFightTheSpeaker(t *testing.T) {
	b, sink, speaker := newTestBridge()
	b.FromSink(Level{Volume: 30}, t0)
	b.FromSpeaker(Level{Volume: 30}, t0)
	for v := 31; v <= 35; v++ {
		b.FromSpeaker(Level{Volume: v}, t0)
	}
	for v := 31; v <= 35; v++ {
		b.FromSink(Level{Volume: v}, t0.Add(100*time.Millisecond))
	}
	if len(speaker.sets) != 0 {
		t.Fatalf("speaker was dragged back: %v", speaker.sets)
	}
	if sink.level.Volume != 35 {
		t.Fatalf("sink = %d", sink.level.Volume)
	}
}

// An old echo is forgotten: the same value set again later is a real change.
func TestEchoExpires(t *testing.T) {
	b, _, speaker := newTestBridge()
	b.FromSpeaker(Level{Volume: 30}, t0)
	b.FromSink(Level{Volume: 30}, t0)
	b.FromSink(Level{Volume: 40}, t0)                     // sent 40
	b.FromSpeaker(Level{Volume: 30}, t0.Add(time.Second)) // turned on the speaker
	b.FromSink(Level{Volume: 30}, t0.Add(time.Second))    // echo
	b.FromSink(Level{Volume: 40}, t0.Add(10*time.Second)) // a real change, long after
	if speaker.level.Volume != 40 || len(speaker.sets) != 2 {
		t.Fatalf("speaker sets = %v", speaker.sets)
	}
}

func TestMuteBothWays(t *testing.T) {
	b, sink, speaker := newTestBridge()
	b.FromSpeaker(Level{Volume: 30}, t0)
	b.FromSink(Level{Volume: 30}, t0)
	b.FromSink(Level{Volume: 30, Muted: true}, t0)
	if !speaker.level.Muted || len(speaker.sets) != 1 {
		t.Fatalf("speaker sets = %v", speaker.sets)
	}
	b.FromSpeaker(Level{Volume: 30, Muted: true}, t0) // echo
	b.FromSpeaker(Level{Volume: 30}, t0.Add(time.Second))
	if sink.level.Muted {
		t.Fatalf("sink still muted")
	}
}
