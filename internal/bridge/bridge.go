package bridge

// Keeps the sink and the speaker at one level. Whichever side last changed is
// the one the other follows, and on (re)connecting the speaker wins: it is
// the thing in the room, and it may have been turned while nothing watched.
//
// Each side echoes what it is told -- set the speaker to 30 and it reports 30
// -- and a slider dragged quickly is several values in flight at once. A
// report that matches a value recently sent that way is taken as its echo and
// not passed back, or a late echo of 30 would drag the slider back from 32.

import (
	"time"
)

// Level is a volume and mute, the same pair on either side.
type Level struct {
	Volume int // 0..100
	Muted  bool
}

const echoWindow = 2 * time.Second

// echoes remembers what was recently sent to one side.
type echoes map[Level]time.Time

func (e echoes) sent(l Level, now time.Time) { e[l] = now }

// isEcho reports, and forgets, whether l was sent within the window.
func (e echoes) isEcho(l Level, now time.Time) bool {
	for k, t := range e {
		if now.Sub(t) > echoWindow {
			delete(e, k)
		}
	}
	if _, ok := e[l]; ok {
		delete(e, l)
		return true
	}
	return false
}

// Side is what the bridge drives on either end.
type Side interface {
	SetVolume(int) error
	SetMute(bool) error
}

type Bridge struct {
	sink, speaker Side
	log           func(msg string, args ...any)

	sinkLevel, speakerLevel *Level
	toSink, toSpeaker       echoes
}

func NewBridge(sink, speaker Side, log func(string, ...any)) *Bridge {
	return &Bridge{sink: sink, speaker: speaker, log: log, toSink: echoes{}, toSpeaker: echoes{}}
}

// FromSink handles the sink reporting l.
func (b *Bridge) FromSink(l Level, now time.Time) {
	if b.toSink.isEcho(l, now) {
		b.sinkLevel = &l
		return
	}
	b.sinkLevel = &l
	// Until the speaker has been heard from, it has the say.
	if b.speakerLevel == nil || *b.speakerLevel == l {
		return
	}
	b.push(b.speaker, b.speakerLevel, l, b.toSpeaker, now, "speaker")
}

// FromSpeaker handles the speaker reporting l.
func (b *Bridge) FromSpeaker(l Level, now time.Time) {
	echo := b.toSpeaker.isEcho(l, now)
	b.speakerLevel = &l
	if echo || b.sinkLevel == nil || *b.sinkLevel == l {
		return
	}
	b.push(b.sink, b.sinkLevel, l, b.toSink, now, "sink")
}

// push moves side from its current level to want, sending only what differs.
func (b *Bridge) push(side Side, current *Level, want Level, sent echoes, now time.Time, name string) {
	if current.Volume != want.Volume {
		if err := side.SetVolume(want.Volume); err != nil {
			b.log("set volume failed", "side", name, "err", err)
			return
		}
	}
	if current.Muted != want.Muted {
		if err := side.SetMute(want.Muted); err != nil {
			b.log("set mute failed", "side", name, "err", err)
			return
		}
	}
	sent.sent(want, now)
	*current = want
}
