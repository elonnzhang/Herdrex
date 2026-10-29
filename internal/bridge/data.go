package bridge

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrinrex/internal/rexdata"
)

const (
	ticketTTL    = 60 * time.Second
	streamKeepMS = 60000
)

type ticket struct {
	pane    string
	session string
	client  *Client
	expires time.Time
}

func hDataConnect(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	sid := p.str("session_id")
	c.mu.Lock()
	ok := c.attached[sid]
	c.mu.Unlock()
	if !ok {
		return nil, newErr("conflict", "connection is not attached to this session", 409)
	}
	if p.str("data_ref_id") != "terminal" {
		return nil, newErr("not_found", "block has no data source "+p.str("data_ref_id"), 404)
	}
	if sp := p.str("subprotocol"); sp != "" && sp != DataProtocol {
		return nil, newErr("bad_request", "unsupported subprotocol "+sp, 400)
	}
	pane, err := b.herdrID(p.str("block_id"), "block")
	if err != nil {
		return nil, err
	}
	var raw [16]byte
	rand.Read(raw[:])
	key := string(raw[:])
	b.mu.Lock()
	for k, t := range b.tickets { // drop expired tickets
		if time.Now().After(t.expires) {
			delete(b.tickets, k)
		}
	}
	b.tickets[key] = ticket{pane: pane, session: sid, client: c, expires: time.Now().Add(ticketTTL)}
	b.mu.Unlock()
	return map[string]any{"ticket": base64.RawURLEncoding.EncodeToString(raw[:]),
		"expires_in_ms": ticketTTL.Milliseconds()}, nil
}

func (b *Bridge) redeem(t [16]byte) (ticket, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tk, ok := b.tickets[string(t[:])]
	delete(b.tickets, string(t[:])) // a ticket works once, even if refused
	if !ok || time.Now().After(tk.expires) {
		return ticket{}, false
	}
	return tk, true
}

func reject(conn *websocket.Conn, reason byte, code websocket.StatusCode) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn.Write(ctx, websocket.MessageBinary, rexdata.Reject(reason))
	conn.Close(code, "data handshake rejected")
}

func (b *Bridge) serveData(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{DataProtocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	hctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	typ, msg, err := conn.Read(hctx)
	cancel()
	hello, ok := rexdata.ParseHello(msg)
	if err != nil || typ != websocket.MessageBinary || !ok {
		reject(conn, rexdata.RejectMalformed, websocket.StatusProtocolError)
		return
	}
	tk, ok := b.redeem(hello)
	if !ok {
		reject(conn, rexdata.RejectUnknownTicket, websocket.StatusPolicyViolation)
		return
	}
	var token [16]byte
	rand.Read(token[:])
	var nb [8]byte
	rand.Read(nb[:])
	nonce := binary.LittleEndian.Uint64(nb[:]) | 1 // never zero
	ctx := conn.CloseRead(r.Context())             // answers pings, ignores client messages
	send := func(p []byte) error {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return conn.Write(wctx, websocket.MessageBinary, p)
	}
	if send(rexdata.Welcome(token, streamKeepMS, nonce)) != nil || send(rexdata.BlankSnapshot) != nil {
		return
	}
	log.Printf("data stream open: pane %s", tk.pane)
	b.stream(ctx, tk, nonce, send, conn)
	log.Printf("data stream closed: pane %s", tk.pane)
}

// stream forwards the pane's terminal frames from its hub, telling the client
// the size before the first frame and whenever it changes.
func (b *Bridge) stream(ctx context.Context, tk ticket, nonce uint64, send func([]byte) error, conn *websocket.Conn) {
	block := b.IDs.Rex("block", tk.pane)
	h, s, err := b.join(ctx, tk.pane, block)
	if err != nil {
		log.Printf("terminal %s: %v", tk.pane, err)
		if isPaneGone(err) || strings.Contains(err.Error(), "not found") {
			conn.Close(closeBlock, "block closed")
		} else {
			conn.Close(websocket.StatusInternalError, "data provider failed")
		}
		return
	}
	defer b.leave(h, s)
	var w, ht uint16
	themed := false
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-s.out:
			if !ok {
				conn.Close(closeReconnct, "data reconnect required")
				return
			}
			if m.theme != nil {
				if w != 0 && send(rexdata.Theme(nonce, m.theme)) != nil {
					return
				}
				continue
			}
			if m.frame == nil {
				if m.exited { // Herdr reports no exit status
					send(rexdata.ChildExited(nonce, 0))
				}
				conn.Close(m.code, m.why)
				return
			}
			f := m.frame
			if f.Width != w || f.Height != ht {
				w, ht = f.Width, f.Height
				cs := h.current()
				if send(rexdata.Resize(nonce, int(w), int(ht), int(cs.CellWidthPx), int(cs.CellHeightPx), b.owner(block))) != nil {
					return
				}
			}
			if !themed { // the theme follows the first resize
				themed = true
				if t := b.theme(block); t != nil && send(rexdata.Theme(nonce, t)) != nil {
					return
				}
			}
			if send(f.Bytes) != nil {
				return
			}
		}
	}
}
