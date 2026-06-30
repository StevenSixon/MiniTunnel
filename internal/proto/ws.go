// WebSocket transport. When the relay can only be reached through an L7 HTTP(S)
// gateway — which terminates TLS with its own certificate and speaks HTTP, not
// raw TLS — the client and agent tunnel through it as a WebSocket instead of
// dialing a raw TCP port. The pinned relay TLS is run as an INNER layer over the
// socket, so the relay's self-signed cert is still pinned end to end and the
// gateway sees only opaque ciphertext. This is the one place MiniTunnel departs
// from stdlib-only (see CLAUDE.md): a correct WebSocket framing implementation
// that survives an uncontrolled intermediary is not worth hand-rolling.
package proto

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

// wsMessageLimit bounds a single inbound WebSocket message. The tunnel carries
// inner TLS records (~16 KiB max), framed one per Write, so this sits well above
// real traffic while still capping memory for a hostile peer that reaches the
// endpoint before the inner TLS + PSK handshake authenticates it.
const wsMessageLimit = 1 << 20

// IsWSURL reports whether addr is a ws:// or wss:// relay URL rather than a plain
// host:port. Callers branch on this to pick the transport.
func IsWSURL(addr string) bool {
	return strings.HasPrefix(addr, "ws://") || strings.HasPrefix(addr, "wss://")
}

// DialRelay opens a connection to the relay, ready for WriteMsg/ReadMsg.
//
// addr is either a plain host:port — a direct TLS dial, optionally through an L4
// gateway — or a ws:// / wss:// URL, tunnelling through an L7 HTTP gateway as a
// WebSocket. In the WebSocket case tlsConf is run as an inner TLS layer over the
// socket so the relay cert is still pinned end to end; the outer wss:// TLS to
// the gateway is verified normally against the system roots. ctx bounds only the
// dial and handshake, not the returned connection's lifetime.
func DialRelay(ctx context.Context, addr string, tlsConf *tls.Config) (net.Conn, error) {
	if !IsWSURL(addr) {
		return tls.DialWithDialer(&net.Dialer{}, "tcp", addr, tlsConf)
	}
	c, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	c.SetReadLimit(wsMessageLimit)
	// context.Background(): the net.Conn must outlive the dial ctx.
	raw := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	inner := tls.Client(raw, tlsConf)
	if err := inner.HandshakeContext(ctx); err != nil {
		inner.Close()
		return nil, fmt.Errorf("inner TLS handshake: %w", err)
	}
	return inner, nil
}

// AcceptWS upgrades an HTTP request to a WebSocket and wraps it in the relay's
// server TLS, returning a connection ready for the relay's normal handler. It is
// the server counterpart to DialRelay's ws:// branch: peers that can reach only
// an L7 HTTP gateway connect here, and the pinned TLS still runs end to end
// inside the socket.
func AcceptWS(w http.ResponseWriter, req *http.Request, serverTLS *tls.Config) (net.Conn, error) {
	// Not a browser, so there is no meaningful Origin to enforce; authentication
	// is the inner TLS + PSK, not the HTTP origin.
	c, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(wsMessageLimit)
	raw := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	return tls.Server(raw, serverTLS), nil
}
