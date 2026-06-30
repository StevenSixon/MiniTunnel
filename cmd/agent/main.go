// Command agent runs on the machine you want to reach (the office Mac mini).
// It dials out to the relay and keeps a control link open, automatically
// reconnecting. When the relay asks for a session it opens a fresh data
// connection and bridges it to a local service (e.g. sshd :22, Screen
// Sharing :5900). Because the agent only ever dials *out*, it works behind NAT
// and survives the office IP changing.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"minitunnel/internal/proto"
)

func main() {
	relayAddr := flag.String("relay", proto.EnvOr("MINITUNNEL_RELAY", ""), "relay address host:port (or MINITUNNEL_RELAY)")
	certFile := flag.String("cert", proto.EnvOr("MINITUNNEL_CERT", "cert.pem"), "pinned relay certificate (or MINITUNNEL_CERT)")
	id := flag.String("id", proto.EnvOr("MINITUNNEL_ID", ""), "this agent's id, chosen by you (or MINITUNNEL_ID)")
	pskFlag := flag.String("psk", "", "pre-shared key (or set MINITUNNEL_PSK)")
	allow := flag.String("allow", proto.EnvOr("MINITUNNEL_ALLOW", "22,5900"), "comma-separated local ports clients may reach (or MINITUNNEL_ALLOW)")
	flag.Parse()

	if *relayAddr == "" || *id == "" {
		log.Fatal("both -relay and -id are required")
	}
	psk := proto.ResolvePSK(*pskFlag)
	if psk == "" {
		log.Fatal("a pre-shared key is required (-psk or MINITUNNEL_PSK)")
	}
	allowed, err := parsePorts(*allow)
	if err != nil {
		log.Fatalf("invalid -allow: %v", err)
	}
	tlsConf, err := proto.ClientTLS(*certFile)
	if err != nil {
		log.Fatalf("loading certificate: %v", err)
	}

	// Reconnect forever: dropped Wi-Fi, relay restart or office IP change all
	// just trigger a re-dial.
	for {
		if err := run(*relayAddr, tlsConf, *id, psk, allowed); err != nil {
			log.Printf("control link lost: %v; retrying in 3s", err)
		}
		time.Sleep(3 * time.Second)
	}
}

func run(addr string, tlsConf *tls.Config, id, psk string, allowed map[int]bool) error {
	conn, err := tls.Dial("tcp", addr, tlsConf)
	if err != nil {
		return err
	}
	defer conn.Close()
	proto.SetKeepAlive(conn)

	if err := proto.WriteMsg(conn, proto.Hello{
		Role:    proto.RoleAgentControl,
		AgentID: id,
		PSK:     psk,
	}); err != nil {
		return err
	}
	log.Printf("registered with relay as %q", id)

	// Heartbeat sender. On write failure, close the conn so the read loop below
	// returns and run() reconnects.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(proto.PingInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if err := proto.WriteMsg(conn, proto.ControlMsg{Type: proto.MsgPing}); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	// Read loop: a Ping is just keepalive; a Notify opens a session. Missing the
	// relay's heartbeat past ControlReadTimeout returns an error and reconnects.
	for {
		conn.SetReadDeadline(time.Now().Add(proto.ControlReadTimeout))
		var m proto.ControlMsg
		if err := proto.ReadMsg(conn, &m); err != nil {
			return err
		}
		if m.Type == proto.MsgNotify {
			go handleSession(addr, tlsConf, psk, m, allowed)
		}
	}
}

func handleSession(addr string, tlsConf *tls.Config, psk string, n proto.ControlMsg, allowed map[int]bool) {
	if !allowed[n.TargetPort] {
		log.Printf("refusing session to disallowed port %d", n.TargetPort)
		return
	}
	relayConn, err := tls.Dial("tcp", addr, tlsConf)
	if err != nil {
		log.Printf("session %s: dial relay: %v", n.SessionID, err)
		return
	}
	if err := proto.WriteMsg(relayConn, proto.Hello{
		Role:      proto.RoleAgentSession,
		SessionID: n.SessionID,
		PSK:       psk,
	}); err != nil {
		relayConn.Close()
		return
	}
	local, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", n.TargetPort))
	if err != nil {
		log.Printf("session %s: dial local :%d: %v", n.SessionID, n.TargetPort, err)
		relayConn.Close()
		return
	}
	log.Printf("session %s -> 127.0.0.1:%d", n.SessionID, n.TargetPort)
	proto.Pipe(relayConn, local)
}

func parsePorts(s string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("bad port %q", part)
		}
		out[p] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ports given")
	}
	return out, nil
}
