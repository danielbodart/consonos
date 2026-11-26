package sonos

// The speaker side: the Sonos local control API, a WebSocket on port 1443
// that the speaker advertises over mDNS (`wss=/websocket/api` in its
// _sonos._tcp record). One connection carries both directions: commands go
// out on it, and once subscribed to playerVolume the speaker pushes every
// change down it, whoever made the change. Nothing listens on this machine.
//
// The same connection puts the speaker back on its TV input -- the optical
// line from this machine -- when asked to. A Sonos leaves that input for
// whatever is played on it from the app or AirPlay, and stays on that source,
// stopped, after it ends: the line is still live, so it never sees the TV
// "come on" and switch back by itself.

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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielbodart/consonos/internal/bridge"
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
	// How long a command that needs its answer waits for it.
	replyTimeout = 5 * time.Second
)

type Speaker struct {
	host string
	root *rsa.PublicKey
	log  *slog.Logger

	mu       sync.Mutex
	conn     *wsConn
	addr     string
	playerID string
	// The certificate name of the speaker first reached, which every later
	// connection must present again.
	deviceID string
	cmdID    atomic.Uint64
	// Commands waiting for their answer, by cmdId.
	pending map[string]chan []byte

	wantTV chan struct{}
}

func NewSpeaker(host string, root *rsa.PublicKey, log *slog.Logger) *Speaker {
	return &Speaker{
		host: host, root: root, log: log,
		pending: map[string]chan []byte{},
		wantTV:  make(chan struct{}, 1),
	}
}

// The public key of "Sonos Device Authentication Root CA", which signs every
// speaker's certificate. Sonos does not publish it and speakers do not send
// it; it was recovered from the signatures on four speakers' certificates
// (RSA signatures s on messages m by one key all satisfy n | s^e - m, so the
// gcd of a few is n), and verifies all of them. See docs/sonos-root.md at the top of the repository.
//
//go:embed root.pem
var RootPEM []byte

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
func (s *Speaker) Run(onLevel func(bridge.Level)) {
	go s.tvInputWorker()
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

func (s *Speaker) session(onLevel func(bridge.Level)) error {
	ip, err := resolve(s.host)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(ip, sonosPort)
	cfg := s.tlsConfig()

	info, err := s.fetchInfo(addr, cfg)
	if err != nil {
		return fmt.Errorf("player info: %w", err)
	}
	playerID := info.PlayerID

	header := http.Header{}
	header.Set("X-Sonos-Api-Key", sonosAPIKey)
	header.Set("Sec-WebSocket-Protocol", sonosProtocol)
	conn, err := dialWebSocket(addr, "/websocket/api", cfg, header, dialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	s.mu.Lock()
	s.conn, s.addr, s.playerID = conn, addr, playerID
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
		if s.deliver(msg) {
			continue
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
func parseVolume(msg []byte) (bridge.Level, bool) {
	var parts []json.RawMessage
	if err := json.Unmarshal(msg, &parts); err != nil || len(parts) != 2 {
		return bridge.Level{}, false
	}
	var body struct {
		Type   string `json:"_objectType"`
		Volume *int   `json:"volume"`
		Muted  *bool  `json:"muted"`
	}
	if err := json.Unmarshal(parts[1], &body); err != nil || body.Type != "playerVolume" || body.Volume == nil || body.Muted == nil {
		return bridge.Level{}, false
	}
	return bridge.Level{Volume: *body.Volume, Muted: *body.Muted}, true
}

func (s *Speaker) SetVolume(v int) error {
	return s.send("setVolume", map[string]any{"volume": v})
}

func (s *Speaker) SetMute(m bool) error {
	return s.send("setMute", map[string]any{"muted": m})
}

var errNotConnected = errors.New("speaker not connected")

func (s *Speaker) send(command string, body map[string]any) error {
	_, err := s.write("playerVolume:1", command, nil, body, nil)
	return err
}

// write sends one command to this player, with ids (householdId, groupId)
// added to its header for the namespaces that need them. A reply channel, if
// given, is registered under the command's cmdId before it goes out.
func (s *Speaker) write(namespace, command string, ids map[string]string, body map[string]any, reply chan []byte) (string, error) {
	s.mu.Lock()
	conn, playerID := s.conn, s.playerID
	s.mu.Unlock()
	if conn == nil {
		return "", errNotConnected
	}
	if body == nil {
		body = map[string]any{}
	}
	cmdID := strconv.FormatUint(s.cmdID.Add(1), 10)
	header := map[string]string{
		"namespace": namespace,
		"command":   command,
		"playerId":  playerID,
		"cmdId":     cmdID,
	}
	for k, v := range ids {
		header[k] = v
	}
	msg, err := json.Marshal([]any{header, body})
	if err != nil {
		return "", err
	}
	if reply != nil {
		s.mu.Lock()
		s.pending[cmdID] = reply
		s.mu.Unlock()
	}
	if err := conn.WriteText(msg); err != nil {
		s.forget(cmdID)
		return "", err
	}
	return cmdID, nil
}

func (s *Speaker) forget(cmdID string) {
	s.mu.Lock()
	delete(s.pending, cmdID)
	s.mu.Unlock()
}

// request sends a command and waits for its answer, returning the answer's
// body, or the speaker's reason if it refused.
func (s *Speaker) request(namespace, command string, ids map[string]string, body map[string]any) (json.RawMessage, error) {
	reply := make(chan []byte, 1)
	cmdID, err := s.write(namespace, command, ids, body, reply)
	if err != nil {
		return nil, err
	}
	defer s.forget(cmdID)
	select {
	case msg := <-reply:
		return parseReply(msg)
	case <-time.After(replyTimeout):
		return nil, fmt.Errorf("%s: no answer", command)
	}
}

// deliver hands a message to the request waiting on its cmdId, if any.
func (s *Speaker) deliver(msg []byte) bool {
	var parts []json.RawMessage
	if err := json.Unmarshal(msg, &parts); err != nil || len(parts) != 2 {
		return false
	}
	var header struct {
		CmdID string `json:"cmdId"`
	}
	if json.Unmarshal(parts[0], &header) != nil || header.CmdID == "" {
		return false
	}
	s.mu.Lock()
	reply, ok := s.pending[header.CmdID]
	delete(s.pending, header.CmdID)
	s.mu.Unlock()
	if ok {
		reply <- msg
	}
	return ok
}

// parseReply returns the body of an answer, or an error carrying the
// speaker's own reason when it says it failed.
func parseReply(msg []byte) (json.RawMessage, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(msg, &parts); err != nil || len(parts) != 2 {
		return nil, fmt.Errorf("malformed answer: %s", msg)
	}
	var header struct {
		Response string `json:"response"`
		Success  bool   `json:"success"`
	}
	if err := json.Unmarshal(parts[0], &header); err != nil {
		return nil, err
	}
	if !header.Success {
		var e struct {
			ErrorCode string `json:"errorCode"`
			Reason    string `json:"reason"`
		}
		json.Unmarshal(parts[1], &e)
		return nil, fmt.Errorf("%s: %s: %s", header.Response, e.ErrorCode, e.Reason)
	}
	return parts[1], nil
}

// WantTVInput asks for the speaker to be on its TV input. It returns at once;
// the check, and the switch if one is needed, happen in the background, and
// asks that arrive meanwhile -- a run of volume steps -- are folded into one.
func (s *Speaker) WantTVInput() {
	select {
	case s.wantTV <- struct{}{}:
	default:
	}
}

func (s *Speaker) tvInputWorker() {
	for range s.wantTV {
		if err := s.ensureTVInput(); err != nil {
			s.log.Warn("TV input not checked", "err", err)
		}
	}
}

// ensureTVInput puts the speaker on its TV input, unless it is on it already:
// switching costs a moment of silence, and the volume keys ask on every step.
// The group is looked up each time, since grouping the speaker with another
// room changes it.
func (s *Speaker) ensureTVInput() error {
	s.mu.Lock()
	addr := s.addr
	connected := s.conn != nil
	s.mu.Unlock()
	if !connected {
		return errNotConnected
	}
	info, err := s.fetchInfo(addr, s.tlsConfig())
	if err != nil {
		return fmt.Errorf("player info: %w", err)
	}
	status, err := s.request("playbackMetadata:1", "getMetadataStatus",
		map[string]string{"householdId": info.HouseholdID, "groupId": info.GroupID}, nil)
	if err != nil {
		return err
	}
	source := containerType(status)
	if isTVInput(source) {
		return nil
	}
	if _, err := s.request("homeTheater:1", "loadHomeTheaterPlayback",
		map[string]string{"householdId": info.HouseholdID}, nil); err != nil {
		return err
	}
	s.log.Info("switched to the TV input", "was", source)
	return nil
}

// containerType is what the group is playing from: "linein.homeTheater.spdif"
// for the optical input, empty when it is stopped on nothing.
func containerType(status json.RawMessage) string {
	var body struct {
		Container struct {
			Type string `json:"type"`
		} `json:"container"`
	}
	json.Unmarshal(status, &body)
	return body.Container.Type
}

func isTVInput(containerType string) bool {
	return strings.HasPrefix(containerType, "linein.homeTheater")
}

type playerInfo struct {
	PlayerID, HouseholdID, GroupID string
}

// fetchInfo asks the speaker who it is, and which household and group it is
// in. Its answer has to agree with the certificate it presented, and on every
// later connection both have to agree with the first.
func (s *Speaker) fetchInfo(addr string, cfg *tls.Config) (playerInfo, error) {
	client := &http.Client{
		Timeout:   dialTimeout,
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest("GET", "https://"+addr+"/api/v1/players/local/info", nil)
	if err != nil {
		return playerInfo{}, err
	}
	req.Header.Set("X-Sonos-Api-Key", sonosAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		return playerInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return playerInfo{}, fmt.Errorf("%s", resp.Status)
	}
	var info struct {
		HouseholdID string `json:"householdId"`
		GroupID     string `json:"groupId"`
		Device      struct {
			ID       string `json:"id"`
			DeviceID string `json:"deviceId"`
		} `json:"device"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return playerInfo{}, err
	}
	cn := resp.TLS.PeerCertificates[0].Subject.CommonName
	if info.Device.ID == "" || info.Device.DeviceID != cn {
		return playerInfo{}, fmt.Errorf("speaker says it is %q but its certificate names %q", info.Device.DeviceID, cn)
	}
	s.mu.Lock()
	if s.deviceID == "" {
		s.deviceID = cn
	}
	s.mu.Unlock()
	return playerInfo{info.Device.ID, info.HouseholdID, info.GroupID}, nil
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
