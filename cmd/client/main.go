// Command client runs on the machine you're sitting at (the home Mac Pro). It
// opens local listening ports and forwards each connection through the relay to
// the named agent's local service. After it's running you simply:
//
//	ssh -p 2222 you@127.0.0.1      # -> agent's :22
//	open vnc://127.0.0.1:5901      # -> agent's :5900 (Screen Sharing)
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/StevenSixon/MiniTunnel/internal/clip"
	"github.com/StevenSixon/MiniTunnel/internal/proto"
)

// multiFlag collects repeated -L flags.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

type forward struct {
	localPort, remotePort int
}

func main() {
	relayAddr := flag.String("relay", proto.EnvOr("MINITUNNEL_RELAY", ""), "relay address host:port (or MINITUNNEL_RELAY)")
	certFile := flag.String("cert", proto.EnvOr("MINITUNNEL_CERT", "cert.pem"), "pinned relay certificate (or MINITUNNEL_CERT)")
	sni := flag.String("sni", proto.EnvOr("MINITUNNEL_SNI", ""), "SNI to send for an L4 gateway that routes by it, e.g. tunnel.example.com; cert is still pinned (or MINITUNNEL_SNI)")
	pskFlag := flag.String("psk", "", "pre-shared key (or set MINITUNNEL_PSK)")
	agentID := flag.String("agent", proto.EnvOr("MINITUNNEL_AGENT", ""), "target agent id (or MINITUNNEL_AGENT)")
	clipPort := flag.String("clip", proto.EnvOr("MINITUNNEL_CLIP", ""), "sync clipboards with the agent via its clipboard port, e.g. 7801; empty disables (or MINITUNNEL_CLIP)")
	var forwards multiFlag
	flag.Var(&forwards, "L", "forward localPort:remotePort (repeatable); default 2222:22 and 5901:5900 (or MINITUNNEL_FORWARD, comma-separated)")
	flag.Parse()

	if *relayAddr == "" || *agentID == "" {
		log.Fatal("both -relay and -agent are required")
	}
	psk := proto.ResolvePSK(*pskFlag)
	if psk == "" {
		log.Fatal("a pre-shared key is required (-psk or MINITUNNEL_PSK)")
	}
	tlsConf, err := proto.ClientTLS(*certFile, *sni)
	if err != nil {
		log.Fatalf("loading certificate: %v", err)
	}

	if len(forwards) == 0 {
		if env := proto.EnvOr("MINITUNNEL_FORWARD", ""); env != "" {
			forwards = multiFlag(strings.Split(env, ","))
		} else {
			forwards = multiFlag{"2222:22", "5901:5900"}
		}
	}
	parsed, err := parseForwards(forwards)
	if err != nil {
		log.Fatalf("invalid -L: %v", err)
	}

	log.Printf("client starting: relay=%s agent=%q forwards=%s", *relayAddr, *agentID, forwards.String())

	// One-time startup probe so an unreachable relay or bad cert/PSK is obvious
	// immediately, rather than only when you try to connect. Non-fatal: the
	// listeners still come up, so it recovers once the relay is back.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 10*time.Second)
	if c, err := proto.DialRelay(probeCtx, *relayAddr, tlsConf); err != nil {
		log.Printf("warning: relay %s not reachable yet: %v (listeners will start anyway)", *relayAddr, err)
	} else {
		c.Close()
		log.Printf("relay %s reachable", *relayAddr)
	}
	cancelProbe()

	var wg sync.WaitGroup
	for _, f := range parsed {
		wg.Add(1)
		go func(f forward) {
			defer wg.Done()
			serve(f, *relayAddr, tlsConf, *agentID, psk)
		}(f)
	}
	if *clipPort != "" {
		p, err := strconv.Atoi(*clipPort)
		if err != nil || p < 1 || p > 65535 {
			log.Fatalf("invalid -clip port %q", *clipPort)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			clipLoop(p, *relayAddr, tlsConf, *agentID, psk)
		}()
	}
	wg.Wait()
}

// clipLoop keeps one clipboard-sync session open to the agent, reconnecting
// like the agent's control link does. The session is an ordinary tunnel
// session to the agent's clipboard port; both ends then run clip.Sync.
func clipLoop(port int, relayAddr string, tlsConf *tls.Config, agentID, psk string) {
	for {
		err := runClipSession(port, relayAddr, tlsConf, agentID, psk)
		log.Printf("clipboard: sync down (%v); retrying in 3s", err)
		time.Sleep(3 * time.Second)
	}
}

func runClipSession(port int, relayAddr string, tlsConf *tls.Config, agentID, psk string) error {
	conn, err := dialRelay(relayAddr, tlsConf, 3)
	if err != nil {
		return err
	}
	defer conn.Close()
	proto.SetKeepAlive(conn)

	if err := proto.WriteMsg(conn, proto.Hello{
		Role:       proto.RoleClientSession,
		AgentID:    agentID,
		PSK:        psk,
		TargetPort: port,
	}); err != nil {
		return err
	}
	var ack proto.SessionAck
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := proto.ReadMsg(conn, &ack); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Time{})
	if !ack.OK {
		return fmt.Errorf("relay refused: %s", ack.Error)
	}
	log.Printf("✓ clipboard sync up with %s (⌘C here ↔ there)", agentID)
	return clip.Sync(conn, "clipboard")
}

func serve(f forward, relayAddr string, tlsConf *tls.Config, agentID, psk string) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", f.localPort))
	if err != nil {
		log.Fatalf("listen :%d: %v", f.localPort, err)
	}
	log.Printf("forwarding 127.0.0.1:%d → %s", f.localPort, target(agentID, f.remotePort))

	for {
		local, err := ln.Accept()
		if err != nil {
			log.Printf("accept on :%d: %v", f.localPort, err)
			continue
		}
		go handleConn(local, f, relayAddr, tlsConf, agentID, psk)
	}
}

// handleConn forwards one accepted local connection through the relay. It
// retries the relay dial briefly (the relay may be momentarily unreachable on a
// flaky VPN) and reads the relay's ack so failures produce a clear log line
// instead of a silent hang.
func handleConn(local net.Conn, f forward, relayAddr string, tlsConf *tls.Config, agentID, psk string) {
	defer local.Close()

	log.Printf("· incoming on 127.0.0.1:%d → %s, opening tunnel …", f.localPort, target(agentID, f.remotePort))

	relayConn, err := dialRelay(relayAddr, tlsConf, 3)
	if err != nil {
		log.Printf(":%d: relay unreachable at %s: %v", f.localPort, relayAddr, err)
		return
	}
	defer func() {
		if relayConn != nil {
			relayConn.Close()
		}
	}()
	proto.SetKeepAlive(relayConn)

	if err := proto.WriteMsg(relayConn, proto.Hello{
		Role:       proto.RoleClientSession,
		AgentID:    agentID,
		PSK:        psk,
		TargetPort: f.remotePort,
	}); err != nil {
		log.Printf(":%d: sending request: %v", f.localPort, err)
		return
	}

	var ack proto.SessionAck
	relayConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := proto.ReadMsg(relayConn, &ack); err != nil {
		log.Printf(":%d: no response from relay (check PSK/certificate): %v", f.localPort, err)
		return
	}
	relayConn.SetReadDeadline(time.Time{})
	if !ack.OK {
		log.Printf(":%d: relay refused: %s", f.localPort, ack.Error)
		return
	}

	log.Printf("✓ CONNECTED  127.0.0.1:%d → %s  — tunnel is up, go ahead 🚀", f.localPort, target(agentID, f.remotePort))
	piped := relayConn
	relayConn = nil // ownership passes to Pipe; suppress the deferred close
	start := time.Now()
	proto.Pipe(local, piped)
	log.Printf("✗ closed     127.0.0.1:%d → %s  after %s", f.localPort, target(agentID, f.remotePort), time.Since(start).Round(time.Second))
}

// target renders the tunnel's far end for logs, naming well-known ports so a
// human reading the client output can tell SSH from Screen Sharing at a glance.
func target(agentID string, remotePort int) string {
	switch remotePort {
	case 22:
		return fmt.Sprintf("%s:%d (SSH)", agentID, remotePort)
	case 5900:
		return fmt.Sprintf("%s:%d (Screen Sharing)", agentID, remotePort)
	}
	return fmt.Sprintf("%s:%d", agentID, remotePort)
}

// dialRelay tries to connect to the relay up to attempts times with a short,
// growing backoff.
func dialRelay(relayAddr string, tlsConf *tls.Config, attempts int) (net.Conn, error) {
	var err error
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var conn net.Conn
		conn, err = proto.DialRelay(ctx, relayAddr, tlsConf)
		cancel()
		if err == nil {
			return conn, nil
		}
		time.Sleep(time.Duration(i+1) * 300 * time.Millisecond)
	}
	return nil, err
}

func parseForwards(specs []string) ([]forward, error) {
	var out []forward
	for _, s := range specs {
		parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("expected localPort:remotePort, got %q", s)
		}
		lp, err1 := strconv.Atoi(parts[0])
		rp, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || lp < 1 || lp > 65535 || rp < 1 || rp > 65535 {
			return nil, fmt.Errorf("bad ports in %q", s)
		}
		out = append(out, forward{localPort: lp, remotePort: rp})
	}
	return out, nil
}
