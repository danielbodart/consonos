package pulse

import (
	"testing"

	"github.com/jfreymuth/pulse/proto"
)

func TestPercentMatchesPactl(t *testing.T) {
	for v := 0; v <= 100; v++ {
		vol := proto.NormVolume(float64(v) / 100)
		if got := percent(proto.ChannelVolumes{vol, vol}); got != v {
			t.Fatalf("%d%% read back as %d", v, got)
		}
	}
	if got := percent(proto.ChannelVolumes{proto.NormVolume(1.5)}); got != 100 {
		t.Fatalf("150%% clamped to %d", got)
	}
}
