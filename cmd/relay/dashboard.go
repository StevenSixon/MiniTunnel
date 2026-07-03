// Status dashboard for the relay: a small read-only HTTP service exposing a
// JSON snapshot of connected agents and live sessions, a self-contained HTML
// page that renders it, and an unauthenticated /healthz for load-balancer /
// container TCP-or-HTTP probes. Everything except /healthz is gated by
// MINITUNNEL_ADMIN_TOKEN. The same listener also hosts the WebSocket tunnel
// entry (<prefix>/tunnel) for relays reached only through an L7 HTTP gateway.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/StevenSixon/MiniTunnel/internal/proto"
)

// normalizePrefix cleans a URL path prefix to "" (root) or "/foo" form — a
// leading slash, no trailing slash — so it can be concatenated with route paths.
func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

// --- JSON shapes returned by /api/status ---

type statusResp struct {
	ListenAddr string          `json:"listen_addr"`
	StartedAt  time.Time       `json:"started_at"`
	UptimeSec  int64           `json:"uptime_sec"`
	Now        time.Time       `json:"now"`
	Agents     []agentStatus   `json:"agents"`
	Sessions   []sessionStatus `json:"sessions"`
}

type agentStatus struct {
	ID             string    `json:"id"`
	RemoteAddr     string    `json:"remote_addr"`
	ConnectedAt    time.Time `json:"connected_at"`
	ConnectedSec   int64     `json:"connected_sec"`
	LastSeenSec    int64     `json:"last_seen_sec"`
	ActiveSessions int       `json:"active_sessions"`
}

type sessionStatus struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	TargetPort  int       `json:"target_port"`
	ClientAddr  string    `json:"client_addr"`
	StartedAt   time.Time `json:"started_at"`
	DurationSec int64     `json:"duration_sec"`
}

// snapshot builds a consistent view of relay state under a single lock.
func (r *relay) snapshot() statusResp {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	perAgent := map[string]int{}
	sessions := make([]sessionStatus, 0, len(r.sessions))
	for _, s := range r.sessions {
		perAgent[s.agentID]++
		sessions = append(sessions, sessionStatus{
			ID:          s.id,
			AgentID:     s.agentID,
			TargetPort:  s.targetPort,
			ClientAddr:  s.clientAddr,
			StartedAt:   s.startedAt,
			DurationSec: int64(now.Sub(s.startedAt).Seconds()),
		})
	}

	agents := make([]agentStatus, 0, len(r.agents))
	for _, a := range r.agents {
		agents = append(agents, agentStatus{
			ID:             a.id,
			RemoteAddr:     a.remoteAddr,
			ConnectedAt:    a.connectedAt,
			ConnectedSec:   int64(now.Sub(a.connectedAt).Seconds()),
			LastSeenSec:    int64(now.Sub(a.lastSeen).Seconds()),
			ActiveSessions: perAgent[a.id],
		})
	}

	return statusResp{
		ListenAddr: r.listenAddr,
		StartedAt:  r.startedAt,
		UptimeSec:  int64(now.Sub(r.startedAt).Seconds()),
		Now:        now,
		Agents:     agents,
		Sessions:   sessions,
	}
}

// handleWS upgrades a WebSocket tunnel request, wraps it in the relay's server
// TLS, and feeds it to the normal connection handler — identical from there on
// to a connection accepted on the raw TLS port.
func (r *relay) handleWS(w http.ResponseWriter, req *http.Request) {
	conn, err := proto.AcceptWS(w, req, r.tlsConf)
	if err != nil {
		log.Printf("ws upgrade from %s failed: %v", req.RemoteAddr, err)
		return
	}
	r.handle(conn)
}

// serveHTTP runs the dashboard listener. Blocks; run in its own goroutine. All
// routes are mounted under r.httpPrefix so the service can sit behind a gateway
// that routes by URL path (e.g. https://host/minitunnel/...).
func (r *relay) serveHTTP(addr string) {
	mux := http.NewServeMux()
	prefix := r.httpPrefix // already normalized in main()

	// Unauthenticated: cheap liveness for a TCP or HTTP probe.
	mux.HandleFunc(prefix+"/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok\n"))
	})

	mux.HandleFunc(prefix+"/api/status", r.authed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r.snapshot())
	}))

	// Tunnel entry for peers stuck behind an L7 HTTP gateway: a WebSocket that
	// carries the same protocol as the raw TLS port. Deliberately NOT behind
	// authed — it carries its own PSK + pinned TLS, and a client has no admin
	// token. The endpoint matters only when the relay sits behind a gateway, so
	// it shares the gateway-routed HTTP listener rather than the raw TLS port.
	mux.HandleFunc(prefix+"/tunnel", r.handleWS)

	// The page knows its own prefix so its fetches and links resolve correctly.
	page := strings.ReplaceAll(dashboardHTML, "__PREFIX__", prefix)
	mux.HandleFunc(prefix+"/", r.authed(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != prefix+"/" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))

	loc := addr + prefix + "/"
	if r.adminToken == "" {
		log.Printf("http dashboard on %s — WARNING: MINITUNNEL_ADMIN_TOKEN unset, dashboard/API disabled (only %s/healthz served)", loc, prefix)
	} else {
		log.Printf("http dashboard on %s (%s/ and %s/api/status protected; %s/healthz open)", loc, prefix, prefix, prefix)
	}
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("http dashboard stopped: %v", err)
	}
}

// authed gates a handler behind MINITUNNEL_ADMIN_TOKEN. The token may arrive as
// an "Authorization: Bearer <t>" header, a "?token=<t>" query param, or an
// "mt_admin" cookie. A valid query token is promoted to a cookie so the page's
// subsequent same-origin fetches authenticate automatically.
func (r *relay) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.adminToken == "" {
			http.Error(w, "dashboard disabled: set MINITUNNEL_ADMIN_TOKEN on the relay", http.StatusServiceUnavailable)
			return
		}
		tok, fromQuery := tokenFromRequest(req)
		if !proto.CheckPSK(tok, r.adminToken) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized: pass the admin token via ?token=…, Authorization: Bearer …, or the mt_admin cookie", http.StatusUnauthorized)
			return
		}
		if fromQuery {
			cookiePath := r.httpPrefix + "/"
			http.SetCookie(w, &http.Cookie{
				Name:     "mt_admin",
				Value:    tok,
				Path:     cookiePath,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
		}
		h(w, req)
	}
}

// tokenFromRequest extracts the admin token and whether it came from the query.
func tokenFromRequest(req *http.Request) (token string, fromQuery bool) {
	if q := req.URL.Query().Get("token"); q != "" {
		return q, true
	}
	if a := req.Header.Get("Authorization"); len(a) > 7 && a[:7] == "Bearer " {
		return a[7:], false
	}
	if c, err := req.Cookie("mt_admin"); err == nil {
		return c.Value, false
	}
	return "", false
}
