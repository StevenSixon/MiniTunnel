package clip

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/StevenSixon/MiniTunnel/internal/proto"
)

// Msg is one framed message on a clipboard-sync connection. It reuses the
// proto.WriteMsg/ReadMsg framing so both ends share the wire helpers.
type Msg struct {
	Type string `json:"type"`           // TypeHello, TypeClip, TypeImg or TypePing
	Text string `json:"text,omitempty"` // clipboard contents for TypeClip

	// TypeImg carries one chunk of a PNG image. Images exceed the 64 KiB frame
	// cap, so they are split into base64 chunks; the receiver reassembles
	// consecutive Seq values until a chunk with More=false arrives. Peers that
	// predate image support simply ignore the type — mixed versions degrade to
	// text-only sync rather than breaking.
	Data string `json:"data,omitempty"` // base64 PNG chunk
	Seq  int    `json:"seq,omitempty"`  // chunk index, 0-based
	More bool   `json:"more,omitempty"` // further chunks follow
}

// Message types. The agent sends one TypeHello immediately after accepting a
// sync session; the client waits for it before declaring the sync up. Without
// the hello a client can't tell "session serving" from "parked connection that
// will never be picked up" (old agent, clip disabled, port not allowlisted) —
// it would sit silent until the read timeout. Sync itself ignores hellos, so
// an old client against a new agent is unaffected.
const (
	TypeHello = "hello"
	TypeClip  = "clip"
	TypeImg   = "img"
	TypePing  = "ping"
)

// HelloTimeout bounds how long the client waits for the agent's hello.
const HelloTimeout = 15 * time.Second

const (
	pollInterval = time.Second // how often each side samples its clipboard
	// maxText caps the synced payload. WriteMsg frames top out at 64 KiB of
	// JSON; 48 KiB of text leaves headroom for JSON escaping. Bigger copies are
	// skipped with a log line rather than killing the link.
	maxText = 48 << 10
	// Image transfer: 32 KiB raw per chunk (~43 KiB base64, inside the frame
	// cap) and 8 MiB per image. The cap keeps a stray huge copy from occupying
	// the link long enough to starve the ping keepalive (peer timeout is 90s).
	imgChunk = 32 << 10
	maxImage = 8 << 20
)

// Sync runs the bidirectional clipboard loop on conn until the link fails.
// Both the agent (per accepted connection) and the client (per tunnel session)
// call this same function — the protocol is symmetric.
//
// Loop prevention: lastSeen holds the hash of the most recent content this end
// either sent or applied. The poller skips content matching lastSeen (so an
// applied remote clipboard isn't echoed straight back), and the reader skips
// incoming content matching lastSeen (so simultaneous copies can't ping-pong).
// Nothing is sent on connect; sync starts with the first *change*, so a stale
// clipboard never stomps the other side.
func Sync(conn net.Conn, tag string) error {
	// Fail fast (and reject the session) if this host can't access a clipboard
	// at all, instead of silently polling errors forever.
	initial, err := Read()
	if err != nil {
		return fmt.Errorf("clipboard unavailable: %w", err)
	}

	var mu sync.Mutex
	lastSeen := hashText(initial)

	var once sync.Once
	stop := func() { once.Do(func() { conn.Close() }) }
	defer stop()
	done := make(chan struct{})
	defer close(done)

	// Writer side: poll the local clipboard and push changes; ping on the same
	// cadence as the control link so L7 gateways don't idle-close the stream.
	go func() {
		poll := time.NewTicker(pollInterval)
		defer poll.Stop()
		ping := time.NewTicker(proto.PingInterval)
		defer ping.Stop()
		lastImgSig := "" // last-seen ImageSig; gates the expensive image fetch
		for {
			select {
			case <-done:
				return
			case <-ping.C:
				if err := proto.WriteMsg(conn, Msg{Type: TypePing}); err != nil {
					stop()
					return
				}
			case <-poll.C:
				// Image first: when the clipboard holds one, the type+size
				// signature decides cheaply whether anything changed; only a
				// changed signature pays for pulling the PNG bytes.
				if sig, ok := ImageSig(); ok {
					if sig == lastImgSig {
						continue
					}
					lastImgSig = sig
					img, err := ReadImage()
					if err != nil {
						continue
					}
					h := hashBytes(img)
					mu.Lock()
					changed := h != lastSeen
					if changed {
						lastSeen = h
					}
					mu.Unlock()
					if !changed {
						continue
					}
					if len(img) > maxImage {
						log.Printf("%s: clipboard image is %d bytes (> %d), not synced", tag, len(img), maxImage)
						continue
					}
					if err := sendImage(conn, img); err != nil {
						stop()
						return
					}
					continue
				}
				lastImgSig = ""
				text, err := Read()
				if err != nil {
					continue // transient (e.g. no GUI session yet); keep polling
				}
				h := hashText(text)
				mu.Lock()
				changed := h != lastSeen
				if changed {
					lastSeen = h
				}
				mu.Unlock()
				if !changed {
					continue
				}
				if len(text) > maxText {
					log.Printf("%s: clipboard is %d bytes (> %d), not synced", tag, len(text), maxText)
					continue
				}
				if err := proto.WriteMsg(conn, Msg{Type: TypeClip, Text: text}); err != nil {
					stop()
					return
				}
			}
		}
	}()

	// Reader side: apply incoming clipboard, treating prolonged silence (no
	// clip and no ping) as a dead link, same policy as the control link.
	var imgBuf []byte // in-flight image reassembly
	nextSeq := 0
	for {
		conn.SetReadDeadline(time.Now().Add(proto.ControlReadTimeout))
		var m Msg
		if err := proto.ReadMsg(conn, &m); err != nil {
			return err
		}
		switch m.Type {
		case TypeClip:
			h := hashText(m.Text)
			mu.Lock()
			if h == lastSeen {
				mu.Unlock()
				continue
			}
			lastSeen = h
			mu.Unlock()
			if err := Write(m.Text); err != nil {
				log.Printf("%s: applying clipboard: %v", tag, err)
			}
		case TypeImg:
			chunk, err := base64.StdEncoding.DecodeString(m.Data)
			if err != nil {
				imgBuf, nextSeq = nil, 0
				continue
			}
			if m.Seq == 0 {
				imgBuf, nextSeq = nil, 0
			}
			if m.Seq != nextSeq || len(imgBuf)+len(chunk) > maxImage {
				imgBuf, nextSeq = nil, 0 // out of order / oversized — drop this transfer
				continue
			}
			imgBuf = append(imgBuf, chunk...)
			nextSeq++
			if m.More {
				continue
			}
			img := imgBuf
			imgBuf, nextSeq = nil, 0
			h := hashBytes(img)
			mu.Lock()
			if h == lastSeen {
				mu.Unlock()
				continue
			}
			lastSeen = h
			mu.Unlock()
			if err := WriteImage(img); err != nil {
				log.Printf("%s: applying image: %v", tag, err)
				continue
			}
			// Calibrate the loop guard against what the pasteboard actually
			// stored: the poller will re-read those bytes on its next tick, and
			// lastSeen must match them (not our wire copy) to prevent an echo.
			if rb, err := ReadImage(); err == nil {
				mu.Lock()
				lastSeen = hashBytes(rb)
				mu.Unlock()
			}
		}
	}
}

// sendImage streams one PNG as consecutive base64 chunks.
func sendImage(conn net.Conn, img []byte) error {
	for i, seq := 0, 0; i < len(img); i, seq = i+imgChunk, seq+1 {
		end := i + imgChunk
		if end > len(img) {
			end = len(img)
		}
		m := Msg{
			Type: TypeImg,
			Data: base64.StdEncoding.EncodeToString(img[i:end]),
			Seq:  seq,
			More: end < len(img),
		}
		if err := proto.WriteMsg(conn, m); err != nil {
			return err
		}
	}
	return nil
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
