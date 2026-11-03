package main

// The speaker side: the Sonos local control API, a WebSocket on port 1443
// that the speaker advertises over mDNS (`wss=/websocket/api` in its
// _sonos._tcp record). One connection carries both directions: commands go
// out on it, and once subscribed to playerVolume the speaker pushes every
// change down it, whoever made the change. Nothing listens on this machine.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sonosPort = "1443"
	// The key every local client sends. It is public -- Home Assistant's
	// sonos-websocket ships it -- and identifies the kind of client rather
	// than authorising anything.
	sonosAPIKey   = "123e4567-e89b-12d3-a456-426655440000"
	sonosProtocol = "v1.api.smartspeaker.audio"
	sonosRootCN   = "Sonos Device Authentication Root CA"

	// The speaker is asked for its volume this often, which doubles as the
	// keepalive: a connection that has heard nothing for readTimeout is dead.
	keepalive   = 30 * time.Second
	readTimeout = 75 * time.Second
	dialTimeout = 10 * time.Second
)

// Level is a volume and mute, the same pair on either side.
type Level struct {
	Volume int // 0..100
	Muted  bool
}

type Speaker struct {
	host string
	root *rsa.PublicKey
	log  *slog.Logger

	mu       sync.Mutex
	conn     *wsConn
	playerID string
	// The certificate name of the speaker first reached, which every later
	// connection must present again.
	deviceID string
	cmdID    atomic.Uint64
}

func NewSpeaker(host string, root *rsa.PublicKey, log *slog.Logger) *Speaker {
	return &Speaker{host: host, root: root, log: log}
}

// The public key of "Sonos Device Authentication Root CA", which signs every
// speaker's certificate. Sonos does not publish it and speakers do not send
// it; it was recovered from the signatures on four speakers' certificates
// (RSA signatures s on messages m by one key all satisfy n | s^e - m, so the
// gcd of a few is n), and verifies all of them. See docs/sonos-root.md.
//
//go:embed sonos-root.pem
var sonosRootPEM []byte

// ParseRootKey reads an RSA public key in PEM (SubjectPublicKeyInfo).
func ParseRootKey(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rsaKey, nil
}

// Run connects, and reconnects for as long as the process lives, calling
// onLevel with the speaker's level on every connect and every change.
func (s *Speaker) Run(onLevel func(Level)) {
	backoff := time.Second
	for {
		start := time.Now()
		err := s.session(onLevel)
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		s.log.Warn("speaker connection lost", "err", err, "retry", backoff)
		time.Sleep(backoff)
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Speaker) session(onLevel func(Level)) error {
	ip, err := resolve(s.host)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(ip, sonosPort)
	cfg := s.tlsConfig()

	playerID, err := s.fetchPlayerID(addr, cfg)
	if err != nil {
		return fmt.Errorf("player info: %w", err)
	}

	header := http.Header{}
	header.Set("X-Sonos-Api-Key", sonosAPIKey)
	header.Set("Sec-WebSocket-Protocol", sonosProtocol)
	conn, err := dialWebSocket(addr, "/websocket/api", cfg, header, dialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	s.mu.Lock()
	s.conn, s.playerID = conn, playerID
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
	}()

	if err := s.send("subscribe", nil); err != nil {
		return err
	}
	if err := s.send("getVolume", nil); err != nil {
		return err
	}
	s.log.Info("speaker connected", "host", s.host, "player", playerID)

	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(keepalive)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := s.send("getVolume", nil); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	for {
		msg, err := conn.ReadMessage(time.Now().Add(readTimeout))
		if err != nil {
			return err
		}
		if level, ok := parseVolume(msg); ok {
			onLevel(level)
		}
	}
}

// resolve turns the speaker's name into an address, afresh on every
// connection so a speaker that moves is followed. The speaker answers 400 to
// any Host header that is not its own address -- office-sonos.lan is refused
// where 10.0.0.167 is not, presumably against DNS rebinding -- so it is only
// ever spoken to by address. IPv4 first, which is what it listens on.
func resolve(host string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			return a, nil
		}
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("%s: no addresses", host)
	}
	return addrs[0], nil
}

// parseVolume picks the playerVolume body out of any message that has one:
// pushed events, and replies to getVolume, look the same.
func parseVolume(msg []byte) (Level, bool) {
	var parts []json.RawMessage
	if err := json.Unmarshal(msg, &parts); err != nil || len(parts) != 2 {
		return Level{}, false
	}
	var body struct {
		Type   string `json:"_objectType"`
		Volume *int   `json:"volume"`
		Muted  *bool  `json:"muted"`
	}
	if err := json.Unmarshal(parts[1], &body); err != nil || body.Type != "playerVolume" || body.Volume == nil || body.Muted == nil {
		return Level{}, false
	}
	return Level{Volume: *body.Volume, Muted: *body.Muted}, true
}

func (s *Speaker) SetVolume(v int) error {
	return s.send("setVolume", map[string]any{"volume": v})
}

func (s *Speaker) SetMute(m bool) error {
	return s.send("setMute", map[string]any{"muted": m})
}

var errNotConnected = errors.New("speaker not connected")

func (s *Speaker) send(command string, body map[string]any) error {
	s.mu.Lock()
	conn, playerID := s.conn, s.playerID
	s.mu.Unlock()
	if conn == nil {
		return errNotConnected
	}
	if body == nil {
		body = map[string]any{}
	}
	msg, err := json.Marshal([]any{
		map[string]string{
			"namespace": "playerVolume:1",
			"command":   command,
			"playerId":  playerID,
			"cmdId":     strconv.FormatUint(s.cmdID.Add(1), 10),
		},
		body,
	})
	if err != nil {
		return err
	}
	return conn.WriteText(msg)
}

// fetchPlayerID asks the speaker who it is. Its answer has to agree with the
// certificate it presented, and on every later connection both have to agree
// with the first.
func (s *Speaker) fetchPlayerID(addr string, cfg *tls.Config) (string, error) {
	client := &http.Client{
		Timeout:   dialTimeout,
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest("GET", "https://"+addr+"/api/v1/players/local/info", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Sonos-Api-Key", sonosAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", resp.Status)
	}
	var info struct {
		Device struct {
			ID       string `json:"id"`
			DeviceID string `json:"deviceId"`
		} `json:"device"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	cn := resp.TLS.PeerCertificates[0].Subject.CommonName
	if info.Device.ID == "" || info.Device.DeviceID != cn {
		return "", fmt.Errorf("speaker says it is %q but its certificate names %q", info.Device.DeviceID, cn)
	}
	s.mu.Lock()
	if s.deviceID == "" {
		s.deviceID = cn
	}
	s.mu.Unlock()
	return info.Device.ID, nil
}

// tlsConfig verifies the speaker's certificate against Sonos's device root
// key, by hand: the speaker sends only its own certificate, and the root is a
// bare key rather than a certificate, so there is nothing for crypto/tls's own
// chain building to work with. Once one speaker has been reached, every later
// connection must also name the same device.
func (s *Speaker) tlsConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate")
			}
			return s.checkLeaf(cs.PeerCertificates[0], time.Now())
		},
	}
}

func (s *Speaker) checkLeaf(leaf *x509.Certificate, now time.Time) error {
	if leaf.Issuer.CommonName != sonosRootCN {
		return fmt.Errorf("certificate issued by %q, not Sonos", leaf.Issuer.CommonName)
	}
	if leaf.SignatureAlgorithm != x509.SHA256WithRSA {
		return fmt.Errorf("certificate signed with %v, not SHA256-RSA", leaf.SignatureAlgorithm)
	}
	digest := sha256.Sum256(leaf.RawTBSCertificate)
	if err := rsa.VerifyPKCS1v15(s.root, crypto.SHA256, digest[:], leaf.Signature); err != nil {
		return errors.New("certificate not signed by the Sonos device root")
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return errors.New("certificate out of date")
	}
	s.mu.Lock()
	want := s.deviceID
	s.mu.Unlock()
	if want != "" && leaf.Subject.CommonName != want {
		return fmt.Errorf("certificate names %q, expected %q", leaf.Subject.CommonName, want)
	}
	return nil
}
