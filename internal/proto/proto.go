// Package proto holds the wire types and small helpers shared by the relay,
// agent and client. The transport is always TLS; every connection opens with a
// single length-prefixed JSON Hello, after which (for data connections) the
// link carries raw bytes.
package proto

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// ServerName is the SAN baked into the relay certificate. Clients pin to this
// name regardless of the relay's (possibly changing) public IP.
const ServerName = "minitunnel-relay"

// Connection roles, sent in the opening Hello.
const (
	RoleAgentControl  = "agent_control"  // agent -> relay, long-lived control link
	RoleAgentSession  = "agent_session"  // agent -> relay, one per tunnelled connection
	RoleClientSession = "client_session" // client -> relay, one per local accept
)

// Hello is the first framed message on every connection.
type Hello struct {
	Role       string `json:"role"`
	AgentID    string `json:"agent_id,omitempty"`
	PSK        string `json:"psk,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TargetPort int    `json:"target_port,omitempty"`
}

// Control message types carried on the long-lived agent control link.
const (
	MsgNotify = "notify" // relay -> agent: open a data connection for a session
	MsgPing   = "ping"   // both directions: liveness heartbeat
)

// ControlMsg is exchanged on the agent control link. Notify carries the
// session details; Ping is a bare keepalive. Both sides send Ping periodically
// and treat silence past ControlReadTimeout as a dead link.
type ControlMsg struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id,omitempty"`
	TargetPort int    `json:"target_port,omitempty"`
}

// SessionAck is the relay's reply to a client_session Hello, sent before the
// connection switches to raw piping. It lets the client report a clear reason
// (e.g. agent offline) instead of hanging on a silently-closed socket.
type SessionAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Heartbeat timing for the control link.
const (
	PingInterval       = 30 * time.Second // how often each side sends a Ping
	ControlReadTimeout = 90 * time.Second // drop the link if nothing arrives within this
)

const maxMsg = 1 << 16

// WriteMsg writes v as a 4-byte big-endian length prefix followed by JSON.
func WriteMsg(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxMsg {
		return fmt.Errorf("message too large: %d bytes", len(b))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadMsg reads one length-prefixed JSON message into v. It never over-reads,
// so the same connection can switch to raw byte piping afterwards.
func ReadMsg(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxMsg {
		return fmt.Errorf("message too large: %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// NewSessionID returns a random 128-bit hex identifier.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// CheckPSK compares two pre-shared keys in constant time.
func CheckPSK(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// ResolvePSK prefers the flag value, falling back to MINITUNNEL_PSK.
func ResolvePSK(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv("MINITUNNEL_PSK")
}

// ClientTLS builds a TLS config that trusts only the pinned relay certificate
// and verifies it against the fixed ServerName.
func ClientTLS(certFile string) (*tls.Config, error) {
	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("no certificate found in %s", certFile)
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: ServerName,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// ServerTLS loads the relay's certificate and key.
func ServerTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// SetKeepAlive enables TCP keepalive on a connection (reaching through a
// *tls.Conn to its underlying socket), so the OS eventually tears down links
// that died without a FIN — defense in depth beneath the application heartbeat.
func SetKeepAlive(conn net.Conn) {
	raw := conn
	if nc, ok := conn.(interface{ NetConn() net.Conn }); ok {
		raw = nc.NetConn()
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		tcp.SetKeepAlive(true)
		tcp.SetKeepAlivePeriod(PingInterval)
	}
}

// closeWriter is satisfied by *net.TCPConn and *tls.Conn, allowing a one-way
// shutdown that propagates EOF to the peer without killing the read side.
type closeWriter interface{ CloseWrite() error }

func halfCloseWrite(c net.Conn) {
	if cw, ok := c.(closeWriter); ok {
		cw.CloseWrite()
	}
}

// Pipe copies bytes in both directions between a and b. Each direction is
// independent: when one side reaches EOF, only the corresponding write half is
// closed (a half-close), so a request/response flow that shuts down its request
// stream early still receives the reply. Both connections are fully closed once
// both directions have finished.
func Pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(a, b)
		halfCloseWrite(a) // b is done sending; tell a's peer no more data
	}()
	go func() {
		defer wg.Done()
		io.Copy(b, a)
		halfCloseWrite(b)
	}()
	wg.Wait()
	a.Close()
	b.Close()
}
