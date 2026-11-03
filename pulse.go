package main

// The desktop side: one sink, read and set over the PulseAudio protocol, which
// PipeWire serves through pipewire-pulse. It is the protocol GNOME's slider,
// the volume keys and Capsper's ducking all land in, so watching the sink here
// sees every one of them.
//
// The sink can be held at full scale on the wire by PipeWire
// (`channelmix.lock-volumes`) and still report the volume it was asked for.
// That reported volume is what is mirrored to the speaker.

import (
	"errors"
	"fmt"
	"math"

	"github.com/jfreymuth/pulse/proto"
)

type Sink struct {
	name    string
	c       *proto.Client
	changed chan struct{}
	closed  chan struct{}
}

// OpenSink connects and subscribes to sink changes. changed receives a signal
// (coalesced) whenever any sink changes; closed is closed if the server goes.
func OpenSink(name string) (*Sink, error) {
	c, conn, err := proto.Connect("")
	if err != nil {
		return nil, err
	}
	s := &Sink{
		name:    name,
		c:       c,
		changed: make(chan struct{}, 1),
		closed:  make(chan struct{}),
	}
	// The callback runs on the client's read loop, so it only signals; a
	// request made from inside it would wait on the loop it is blocking.
	c.Callback = func(msg any) {
		switch msg := msg.(type) {
		case *proto.SubscribeEvent:
			if msg.Event&proto.EventFacilityMask == proto.EventSink {
				select {
				case s.changed <- struct{}{}:
				default:
				}
			}
		case *proto.ConnectionClosed:
			close(s.closed)
		}
	}
	props := proto.PropList{
		"application.name":      proto.PropListString("consonos"),
		"application.icon_name": proto.PropListString("audio-speakers"),
	}
	if err := c.Request(&proto.SetClientName{Props: props}, &proto.SetClientNameReply{}); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.Request(&proto.Subscribe{Mask: proto.SubscriptionMaskSink}, nil); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

func (s *Sink) info() (*proto.GetSinkInfoReply, error) {
	var info proto.GetSinkInfoReply
	if err := s.c.Request(&proto.GetSinkInfo{SinkIndex: proto.Undefined, SinkName: s.name}, &info); err != nil {
		return nil, fmt.Errorf("sink %s: %w", s.name, err)
	}
	return &info, nil
}

// Level is the sink's volume as the percentage GNOME shows, and its mute.
func (s *Sink) Level() (Level, error) {
	info, err := s.info()
	if err != nil {
		return Level{}, err
	}
	return Level{Volume: percent(info.ChannelVolumes), Muted: info.Mute}, nil
}

func (s *Sink) SetVolume(v int) error {
	info, err := s.info()
	if err != nil {
		return err
	}
	if len(info.ChannelVolumes) == 0 {
		return errors.New("sink has no channels")
	}
	vols := make(proto.ChannelVolumes, len(info.ChannelVolumes))
	for i := range vols {
		vols[i] = proto.NormVolume(float64(v) / 100)
	}
	return s.c.Request(&proto.SetSinkVolume{SinkIndex: proto.Undefined, SinkName: s.name, ChannelVolumes: vols}, nil)
}

func (s *Sink) SetMute(m bool) error {
	return s.c.Request(&proto.SetSinkMute{SinkIndex: proto.Undefined, SinkName: s.name, Mute: m}, nil)
}

// percent is the loudest channel as the whole-number percentage pactl and
// GNOME show, clamped to the speaker's 0..100.
func percent(vols proto.ChannelVolumes) int {
	var hi proto.Volume
	for _, v := range vols {
		hi = max(hi, v)
	}
	p := int(math.Round(hi.Norm() * 100))
	return min(max(p, 0), 100)
}
