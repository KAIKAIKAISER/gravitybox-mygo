package cdp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsConn is a minimal RFC 6455 loopback WebSocket client that omits browser Origin headers
// so Chromium/Electron DevTools endpoints accept connections without --remote-allow-origins=*.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

func dialWS(ctx context.Context, rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported websocket scheme: %s", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	secKey := base64.StdEncoding.EncodeToString(keyBytes)

	reqURI := u.RequestURI()
	if reqURI == "" {
		reqURI = "/"
	}
	var reqBuf strings.Builder
	reqBuf.WriteString("GET " + reqURI + " HTTP/1.1\r\n")
	reqBuf.WriteString("Host: " + u.Host + "\r\n")
	reqBuf.WriteString("Upgrade: websocket\r\n")
	reqBuf.WriteString("Connection: Upgrade\r\n")
	reqBuf.WriteString("Sec-WebSocket-Key: " + secKey + "\r\n")
	reqBuf.WriteString("Sec-WebSocket-Version: 13\r\n\r\n")

	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := io.WriteString(conn, reqBuf.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}

	br := bufio.NewReaderSize(conn, 64*1024)
	dummyReq, _ := http.NewRequest(http.MethodGet, "http://"+u.Host+reqURI, nil)
	resp, err := http.ReadResponse(br, dummyReq)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake rejected: HTTP %d", resp.StatusCode)
	}

	sum := sha1.Sum([]byte(secKey + wsGUID))
	expectedAccept := base64.StdEncoding.EncodeToString(sum[:])
	if resp.Header.Get("Sec-WebSocket-Accept") != expectedAccept {
		_ = conn.Close()
		return nil, errors.New("websocket handshake invalid Sec-WebSocket-Accept")
	}
	_ = conn.SetDeadline(time.Time{})

	return &wsConn{conn: conn, br: br}, nil
}

func (w *wsConn) Close() error {
	return w.conn.Close()
}

func (w *wsConn) WriteText(msg string) error {
	return w.writeFrame(0x1, []byte(msg))
}

func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()

	n := len(payload)
	var hdr [14]byte
	hdr[0] = 0x80 | (opcode & 0x0f)
	hLen := 2
	switch {
	case n < 126:
		hdr[1] = 0x80 | byte(n)
	case n <= 0xffff:
		hdr[1] = 0x80 | 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(n))
		hLen = 4
	default:
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(n))
		hLen = 10
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	copy(hdr[hLen:hLen+4], mask[:])
	hLen += 4

	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i&3]
	}

	if _, err := w.conn.Write(hdr[:hLen]); err != nil {
		return err
	}
	if n > 0 {
		if _, err := w.conn.Write(masked); err != nil {
			return err
		}
	}
	return nil
}

func (w *wsConn) ReadText() (string, error) {
	var msgBuf []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			return "", err
		}
		fin := (h[0] & 0x80) != 0
		opcode := h[0] & 0x0f
		masked := (h[1] & 0x80) != 0
		plen := uint64(h[1] & 0x7f)

		switch plen {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(w.br, ext[:]); err != nil {
				return "", err
			}
			plen = uint64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(w.br, ext[:]); err != nil {
				return "", err
			}
			plen = binary.BigEndian.Uint64(ext[:])
		}
		if plen > 64*1024*1024 {
			return "", errors.New("websocket frame too large")
		}

		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(w.br, mask[:]); err != nil {
				return "", err
			}
		}
		payload := make([]byte, int(plen))
		if plen > 0 {
			if _, err := io.ReadFull(w.br, payload); err != nil {
				return "", err
			}
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i&3]
			}
		}

		switch opcode {
		case 0x8: // Close
			return "", io.EOF
		case 0x9: // Ping
			_ = w.writeFrame(0xA, payload)
			continue
		case 0xA: // Pong
			continue
		case 0x0, 0x1, 0x2:
			msgBuf = append(msgBuf, payload...)
			if fin {
				return string(msgBuf), nil
			}
		}
	}
}
