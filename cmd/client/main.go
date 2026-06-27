// Command client runs on the machine you're sitting at (the home Mac Pro). It
// opens local listening ports and forwards each connection through the relay to
// the named agent's local service. After it's running you simply:
//
//	ssh -p 2222 you@127.0.0.1      # -> agent's :22
//	open vnc://127.0.0.1:5901      # -> agent's :5900 (Screen Sharing)
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"minitunnel/internal/proto"
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
	relayAddr := flag.String("relay", "", "relay address host:port")
	certFile := flag.String("cert", "cert.pem", "pinned relay certificate")
	pskFlag := flag.String("psk", "", "pre-shared key (or set MINITUNNEL_PSK)")
	agentID := flag.String("agent", "", "target agent id")
	var forwards multiFlag
	flag.Var(&forwards, "L", "forward localPort:remotePort (repeatable); default 2222:22 and 5901:5900")
	flag.Parse()

	if *relayAddr == "" || *agentID == "" {
		log.Fatal("both -relay and -agent are required")
	}
	psk := proto.ResolvePSK(*pskFlag)
	if psk == "" {
		log.Fatal("a pre-shared key is required (-psk or MINITUNNEL_PSK)")
	}
	tlsConf, err := proto.ClientTLS(*certFile)
	if err != nil {
		log.Fatalf("loading certificate: %v", err)
	}

	if len(forwards) == 0 {
		forwards = multiFlag{"2222:22", "5901:5900"}
	}
	parsed, err := parseForwards(forwards)
	if err != nil {
		log.Fatalf("invalid -L: %v", err)
	}

	// One-time startup probe so an unreachable relay or bad cert/PSK is obvious
	// immediately, rather than only when you try to connect. Non-fatal: the
	// listeners still come up, so it recovers once the relay is back.
	if c, err := tls.Dial("tcp", *relayAddr, tlsConf); err != nil {
		log.Printf("warning: relay %s not reachable yet: %v (listeners will start anyway)", *relayAddr, err)
	} else {
		c.Close()
		log.Printf("relay %s reachable", *relayAddr)
	}

	var wg sync.WaitGroup
	for _, f := range parsed {
		wg.Add(1)
		go func(f forward) {
			defer wg.Done()
			serve(f, *relayAddr, tlsConf, *agentID, psk)
		}(f)
	}
	wg.Wait()
}

func serve(f forward, relayAddr string, tlsConf *tls.Config, agentID, psk string) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", f.localPort))
	if err != nil {
		log.Fatalf("listen :%d: %v", f.localPort, err)
	}
	log.Printf("forwarding 127.0.0.1:%d -> agent %q :%d", f.localPort, agentID, f.remotePort)

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

	piped := relayConn
	relayConn = nil // ownership passes to Pipe; suppress the deferred close
	proto.Pipe(local, piped)
}

// dialRelay tries to connect to the relay up to attempts times with a short,
// growing backoff.
func dialRelay(relayAddr string, tlsConf *tls.Config, attempts int) (net.Conn, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var conn net.Conn
		conn, err = tls.Dial("tcp", relayAddr, tlsConf)
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
