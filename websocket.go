package main

// A WebSocket client of exactly the size the Sonos needs: one connection,
// text frames out, text frames in, pings answered. RFC 6455, client side only,
// on top of the standard library, so the TLS settings it dials with are the
// ones in sonos.go and nothing else.

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa

	// Sonos messages are a few hundred bytes; anything near this is not one.
	maxMessage = 1 << 20

	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
}

// dialWebSocket opens wss://addr+path with the given TLS config and extra
// request headers, and completes the opening handshake.
func dialWebSocket(addr, path string, cfg *tls.Config, header http.Header, timeout time.Duration) (*wsConn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, cfg)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(timeout))

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])

	req, err := http.NewRequest("GET", "https://"+addr+path, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}

	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: %s", resp.Status)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), acceptKey(key); got != want {
		conn.Close()
		return nil, errors.New("websocket handshake: bad Sec-WebSocket-Accept")
	}

	conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, r: r}, nil
}

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// WriteText sends one unfragmented text message. Safe for concurrent use.
func (c *wsConn) WriteText(p []byte) error {
	return c.writeFrame(opText, p)
}

func (c *wsConn) writeFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	frame, err := encodeClientFrame(op, payload)
	if err != nil {
		return err
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = c.conn.Write(frame)
	return err
}

// encodeClientFrame builds a single FIN frame, masked as RFC 6455 requires of
// every frame a client sends.
func encodeClientFrame(op byte, payload []byte) ([]byte, error) {
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return nil, err
	}
	n := len(payload)
	frame := make([]byte, 0, 14+n)
	frame = append(frame, 0x80|op)
	switch {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	case n <= 0xffff:
		frame = append(frame, 0x80|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(n))
	default:
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(n))
	}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	return frame, nil
}

// ReadMessage returns the next text or binary message, reassembling
// fragments and answering pings on the way. A close frame ends it with
// io.EOF. The deadline covers the whole message.
func (c *wsConn) ReadMessage(deadline time.Time) ([]byte, error) {
	c.conn.SetReadDeadline(deadline)
	var msg []byte
	inMessage := false
	for {
		fin, op, payload, err := readFrame(c.r)
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
		case opPong:
		case opClose:
			c.writeFrame(opClose, nil)
			return nil, io.EOF
		case opText, opBinary, opContinuation:
			if (op == opContinuation) != inMessage {
				return nil, errors.New("websocket: unexpected continuation")
			}
			inMessage = true
			if len(msg)+len(payload) > maxMessage {
				return nil, errors.New("websocket: message too large")
			}
			msg = append(msg, payload...)
			if fin {
				return msg, nil
			}
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %#x", op)
		}
	}
}

// readFrame reads one frame from a server, which must not mask.
func readFrame(r io.Reader) (fin bool, op byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}
	fin = hdr[0]&0x80 != 0
	if hdr[0]&0x70 != 0 {
		err = errors.New("websocket: reserved bits set")
		return
	}
	op = hdr[0] & 0x0f
	if hdr[1]&0x80 != 0 {
		err = errors.New("websocket: masked frame from server")
		return
	}
	n := uint64(hdr[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if op >= opClose && (n > 125 || !fin) {
		err = errors.New("websocket: bad control frame")
		return
	}
	if n > maxMessage {
		err = errors.New("websocket: frame too large")
		return
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return
}

func (c *wsConn) Close() error {
	return c.conn.Close()
}
