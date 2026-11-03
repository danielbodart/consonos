package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfreymuth/pulse/proto"
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

func TestParseVolume(t *testing.T) {
	// As the Ray sends them: a pushed event and a reply to getVolume.
	event := `[{"namespace":"playerVolume:1","name":"playerVolume","type":"playerVolume"},{"_objectType":"playerVolume","volume":26,"muted":false,"fixed":false}]`
	reply := `[{"namespace":"playerVolume:1","response":"getVolume","success":true,"type":"playerVolume","cmdId":"2"},{"_objectType":"playerVolume","volume":40,"muted":true,"fixed":false}]`
	subscribed := `[{"namespace":"playerVolume:1","response":"subscribe","success":true,"type":"none","cmdId":"1"},{}]`

	if l, ok := parseVolume([]byte(event)); !ok || l != (Level{Volume: 26}) {
		t.Fatalf("event: %v %v", l, ok)
	}
	if l, ok := parseVolume([]byte(reply)); !ok || l != (Level{Volume: 40, Muted: true}) {
		t.Fatalf("reply: %v %v", l, ok)
	}
	if _, ok := parseVolume([]byte(subscribed)); ok {
		t.Fatal("subscribe reply read as a level")
	}
}

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

func loadCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testSpeaker(t *testing.T) *Speaker {
	t.Helper()
	root, err := ParseRootKey(sonosRootPEM)
	if err != nil {
		t.Fatal(err)
	}
	return NewSpeaker("x", root, nil)
}

// Four real speakers' certificates -- a Ray, a One and two Play:1s -- all
// verify against the embedded root.
func TestRealCertificatesVerify(t *testing.T) {
	paths, _ := filepath.Glob("testdata/speaker*.pem")
	if len(paths) < 4 {
		t.Fatalf("found %d test certificates", len(paths))
	}
	for _, p := range paths {
		c := loadCert(t, p)
		if err := testSpeaker(t).checkLeaf(c, c.NotBefore.Add(time.Hour)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestCheckLeafRejects(t *testing.T) {
	c := loadCert(t, "testdata/speaker1.pem")
	in := c.NotBefore.Add(time.Hour)

	tampered := *c
	tampered.RawTBSCertificate = bytes.Clone(c.RawTBSCertificate)
	tampered.RawTBSCertificate[len(tampered.RawTBSCertificate)-1] ^= 1
	if err := testSpeaker(t).checkLeaf(&tampered, in); err == nil {
		t.Error("accepted a certificate whose contents were changed")
	}

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSpeaker("x", &other.PublicKey, nil)
	if err := s.checkLeaf(c, in); err == nil {
		t.Error("accepted a certificate under a different root")
	}

	if err := testSpeaker(t).checkLeaf(c, c.NotAfter.Add(time.Hour)); err == nil {
		t.Error("accepted an expired certificate")
	}

	s = testSpeaker(t)
	s.deviceID = "000000000000"
	if err := s.checkLeaf(c, in); err == nil {
		t.Error("accepted a different speaker than the one first reached")
	}
}
