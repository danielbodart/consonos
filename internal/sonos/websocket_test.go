package sonos

import (
	"bytes"
	"testing"
)

func TestAcceptKey(t *testing.T) {
	// The worked example in RFC 6455 section 1.3.
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal(got)
	}
}

// unmask turns a client frame into the server frame with the same payload, so
// readFrame can read back what encodeClientFrame wrote.
func unmask(t *testing.T, frame []byte) []byte {
	t.Helper()
	n := int(frame[1] & 0x7f)
	off := 2
	switch n {
	case 126:
		off = 4
	case 127:
		off = 10
	}
	mask := frame[off : off+4]
	out := append([]byte{frame[0], frame[1] & 0x7f}, frame[2:off]...)
	for i, b := range frame[off+4:] {
		out = append(out, b^mask[i%4])
	}
	return out
}

func TestFrameRoundTrip(t *testing.T) {
	for _, n := range []int{0, 5, 125, 126, 65535, 65536} {
		payload := bytes.Repeat([]byte{'x'}, n)
		frame, err := encodeClientFrame(opText, payload)
		if err != nil {
			t.Fatal(err)
		}
		if frame[1]&0x80 == 0 {
			t.Fatalf("%d: client frame not masked", n)
		}
		fin, op, got, err := readFrame(bytes.NewReader(unmask(t, frame)))
		if err != nil || !fin || op != opText || !bytes.Equal(got, payload) {
			t.Fatalf("%d: fin=%v op=%d len=%d err=%v", n, fin, op, len(got), err)
		}
	}
}

func TestReadFrameRejectsMaskedServerFrame(t *testing.T) {
	if _, _, _, err := readFrame(bytes.NewReader([]byte{0x81, 0x80, 0, 0, 0, 0})); err == nil {
		t.Fatal("accepted a masked frame from the server")
	}
}
