// Command relay runs on a public VPS. It is the rendezvous point: agents dial
// in and stay connected; clients ask for a tunnel to a named agent; the relay
// matches the two data connections and pipes bytes between them.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"minitunnel/internal/proto"
)

// agentConn is a registered agent's long-lived control link. The mutex
// serializes control writes (Notify from client requests, plus the heartbeat).
type agentConn struct {
	id   string
	conn net.Conn
	mu   sync.Mutex
}

type relay struct {
	psk string

	mu      sync.Mutex
	agents  map[string]*agentConn // agentID -> control link
	waiting map[string]net.Conn   // sessionID -> parked client connection
}

func main() {
	addr := flag.String("addr", ":7000", "TLS listen address")
	certFile := flag.String("cert", "cert.pem", "relay certificate")
	keyFile := flag.String("key", "key.pem", "relay private key")
	pskFlag := flag.String("psk", "", "pre-shared key (or set MINITUNNEL_PSK)")
	flag.Parse()

	psk := proto.ResolvePSK(*pskFlag)
	if psk == "" {
		log.Fatal("a pre-shared key is required (-psk or MINITUNNEL_PSK)")
	}
	tlsConf, err := proto.ServerTLS(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("loading certificate: %v", err)
	}
	ln, err := tls.Listen("tcp", *addr, tlsConf)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	log.Printf("relay listening on %s", *addr)

	r := &relay{
		psk:     psk,
		agents:  map[string]*agentConn{},
		waiting: map[string]net.Conn{},
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go r.handle(conn)
	}
}

func (r *relay) handle(conn net.Conn) {
	// A slow or hostile peer must not hold a connection open before identifying.
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var h proto.Hello
	if err := proto.ReadMsg(conn, &h); err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})

	if !proto.CheckPSK(h.PSK, r.psk) {
		log.Printf("rejected %s: bad pre-shared key", conn.RemoteAddr())
		conn.Close()
		return
	}

	switch h.Role {
	case proto.RoleAgentControl:
		r.handleAgent(conn, h)
	case proto.RoleClientSession:
		r.handleClient(conn, h)
	case proto.RoleAgentSession:
		r.handleAgentSession(conn, h)
	default:
		conn.Close()
	}
}

// handleAgent registers a control link and keeps it alive with a heartbeat: it
// sends a Ping every PingInterval and drops the agent if nothing arrives within
// ControlReadTimeout. This detects links silently severed by NAT/firewall
// timeouts far faster than waiting on a TCP error.
func (r *relay) handleAgent(conn net.Conn, h proto.Hello) {
	if h.AgentID == "" {
		conn.Close()
		return
	}
	proto.SetKeepAlive(conn)
	ac := &agentConn{id: h.AgentID, conn: conn}

	r.mu.Lock()
	if old := r.agents[h.AgentID]; old != nil {
		old.conn.Close() // replace a stale registration
	}
	r.agents[h.AgentID] = ac
	r.mu.Unlock()
	log.Printf("agent %q registered from %s", h.AgentID, conn.RemoteAddr())

	stop := make(chan struct{})
	defer close(stop)
	defer func() {
		r.mu.Lock()
		if r.agents[h.AgentID] == ac {
			delete(r.agents, h.AgentID)
		}
		r.mu.Unlock()
		conn.Close()
		log.Printf("agent %q disconnected", h.AgentID)
	}()

	// Heartbeat sender. On write failure, close the conn so the read loop below
	// unblocks and the agent is deregistered.
	go func() {
		t := time.NewTicker(proto.PingInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ac.mu.Lock()
				err := proto.WriteMsg(ac.conn, proto.ControlMsg{Type: proto.MsgPing})
				ac.mu.Unlock()
				if err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	// Read loop: any inbound message (the agent's own Ping) resets the deadline.
	for {
		conn.SetReadDeadline(time.Now().Add(proto.ControlReadTimeout))
		var m proto.ControlMsg
		if err := proto.ReadMsg(conn, &m); err != nil {
			return
		}
	}
}

// handleClient acknowledges the request, parks the client connection, and asks
// the target agent to dial back. The actual piping happens in
// handleAgentSession once the agent's data connection arrives.
func (r *relay) handleClient(conn net.Conn, h proto.Hello) {
	r.mu.Lock()
	ac := r.agents[h.AgentID]
	r.mu.Unlock()
	if ac == nil {
		proto.WriteMsg(conn, proto.SessionAck{OK: false, Error: fmt.Sprintf("agent %q is not online", h.AgentID)})
		log.Printf("client wants offline agent %q", h.AgentID)
		conn.Close()
		return
	}

	sid, err := proto.NewSessionID()
	if err != nil {
		proto.WriteMsg(conn, proto.SessionAck{OK: false, Error: "relay error"})
		conn.Close()
		return
	}

	// Park before notifying so a fast agent dial-back can't miss the session.
	r.mu.Lock()
	r.waiting[sid] = conn
	r.mu.Unlock()

	ac.mu.Lock()
	err = proto.WriteMsg(ac.conn, proto.ControlMsg{Type: proto.MsgNotify, SessionID: sid, TargetPort: h.TargetPort})
	ac.mu.Unlock()
	if err != nil {
		r.mu.Lock()
		delete(r.waiting, sid)
		r.mu.Unlock()
		proto.WriteMsg(conn, proto.SessionAck{OK: false, Error: "agent unreachable"})
		conn.Close()
		return
	}

	if err := proto.WriteMsg(conn, proto.SessionAck{OK: true}); err != nil {
		r.mu.Lock()
		delete(r.waiting, sid)
		r.mu.Unlock()
		conn.Close()
		return
	}

	// Reap the parked connection if the agent never dials back.
	go func() {
		time.Sleep(15 * time.Second)
		r.mu.Lock()
		if c := r.waiting[sid]; c == conn {
			delete(r.waiting, sid)
			c.Close()
			log.Printf("session %s timed out waiting for agent", sid)
		}
		r.mu.Unlock()
	}()
}

// handleAgentSession matches an agent data connection with its parked client
// and pipes them together.
func (r *relay) handleAgentSession(conn net.Conn, h proto.Hello) {
	r.mu.Lock()
	client := r.waiting[h.SessionID]
	delete(r.waiting, h.SessionID)
	r.mu.Unlock()
	if client == nil {
		log.Printf("agent_session for unknown/expired session %s", h.SessionID)
		conn.Close()
		return
	}
	log.Printf("pairing session %s", h.SessionID)
	proto.Pipe(client, conn)
}
