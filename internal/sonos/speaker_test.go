package sonos

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

	"github.com/danielbodart/consonos/internal/bridge"
)

func TestParseVolume(t *testing.T) {
	// As the Ray sends them: a pushed event and a reply to getVolume.
	event := `[{"namespace":"playerVolume:1","name":"playerVolume","type":"playerVolume"},{"_objectType":"playerVolume","volume":26,"muted":false,"fixed":false}]`
	reply := `[{"namespace":"playerVolume:1","response":"getVolume","success":true,"type":"playerVolume","cmdId":"2"},{"_objectType":"playerVolume","volume":40,"muted":true,"fixed":false}]`
	subscribed := `[{"namespace":"playerVolume:1","response":"subscribe","success":true,"type":"none","cmdId":"1"},{}]`

	if l, ok := parseVolume([]byte(event)); !ok || l != (bridge.Level{Volume: 26}) {
		t.Fatalf("event: %v %v", l, ok)
	}
	if l, ok := parseVolume([]byte(reply)); !ok || l != (bridge.Level{Volume: 40, Muted: true}) {
		t.Fatalf("reply: %v %v", l, ok)
	}
	if _, ok := parseVolume([]byte(subscribed)); ok {
		t.Fatal("subscribe reply read as a level")
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
	root, err := ParseRootKey(RootPEM)
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
