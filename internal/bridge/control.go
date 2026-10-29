package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

const (
	ControlProtocol = "rex.control.v1"
	DataProtocol    = "rex.data.terminal.v1"
	APIRevision     = 7
	Version         = "herdr-in-rex/0.1" // reported as server.version rex_version
)

type Client struct {
	ID   string
	conn *websocket.Conn

	mu          sync.Mutex
	seq         uint64
	attached    map[string]bool
	attachedAt  map[string]time.Time
	attachAll   bool
	eventAll    bool           // attach_all with event_scope "all"
	info        map[string]any // the hello's client object
	principal   map[string]any
	connectedAt time.Time
}

func (c *Client) wants(sid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attachAll || c.attached[sid]
}

// wantsBlockEvents: block_event goes to clients attached to the session or
// attached to everything with event_scope "all".
func (c *Client) wantsBlockEvents(sid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventAll || c.attached[sid]
}

func (c *Client) isAttachAll() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attachAll
}

func (c *Client) currentSeq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

func (c *Client) write(v any) error {
	b, err := typeFirst(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, b)
}

func (c *Client) push(payload any) {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()
	if os.Getenv("HERDR_IN_REX_TRACE") != "" {
		log.Printf("event client=%s seq=%d %v", c.ID, seq, payload)
	}
	if err := c.write(map[string]any{"type": "event", "seq": seq, "payload": payload}); err != nil {
		c.conn.Close(websocket.StatusGoingAway, "write failed")
	}
}

// keyRank orders envelope keys the way the real Rex server writes them:
// tag and routing fields first, the body last.
var keyRank = map[string]int{"type": -4, "id": -3, "seq": -2, "session_id": -1, "payload": 1, "error": 1}

// typeFirst marshals v like json.Marshal, but orders map keys by keyRank and
// then by name, so messages match the real server's field order.
func typeFirst(v any) ([]byte, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keyRank[keys[i]] != keyRank[keys[j]] {
				return keyRank[keys[i]] < keyRank[keys[j]]
			}
			return keys[i] < keys[j]
		})
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			vb, err := typeFirst(x[k])
			if err != nil {
				return nil, err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			buf.Write(vb)
		}
		buf.WriteByte('}')
		return buf.Bytes(), nil
	case []any:
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			eb, err := typeFirst(e)
			if err != nil {
				return nil, err
			}
			buf.Write(eb)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	}
	return json.Marshal(v)
}

type rexError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *rexError) Error() string { return e.Code + ": " + e.Message }

func newErr(code, msg string, status int) *rexError { return &rexError{code, msg, status} }

type request struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

// Handler serves HTTP: /api/control and /api/data.
func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/control", b.serveControl)
	mux.HandleFunc("/api/data", b.serveData)
	return mux
}

func (b *Bridge) serveControl(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{ControlProtocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != ControlProtocol {
		conn.Close(websocket.StatusProtocolError, "protocol error")
		return
	}
	conn.SetReadLimit(1 << 20)
	c := &Client{ID: "client:" + uuid.NewString(), conn: conn, attached: map[string]bool{},
		attachedAt: map[string]time.Time{}, principal: principal(r.RemoteAddr), connectedAt: time.Now()}
	ctx := r.Context()
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	typ, data, err := conn.Read(hctx)
	cancel()
	var hello struct {
		Type   string
		Client map[string]any
	}
	if err != nil || typ != websocket.MessageText || json.Unmarshal(data, &hello) != nil || hello.Type != "hello" {
		conn.Close(websocket.StatusProtocolError, "protocol error")
		return
	}
	tok := make([]byte, 16)
	rand.Read(tok)
	c.write(map[string]any{"type": "welcome", "protocol": ControlProtocol,
		"limits":       map[string]any{"max_message_bytes": 1 << 20},
		"client":       map[string]any{"id": c.ID, "reconnect_token": base64.RawURLEncoding.EncodeToString(tok)},
		"api_revision": APIRevision, "api_server_revision": APIRevision})
	c.info = hello.Client
	if c.info == nil {
		c.info = map[string]any{}
	}
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
	b.joined(c)
	defer func() {
		b.mu.Lock()
		delete(b.clients, c)
		b.mu.Unlock()
		b.left(c)
		b.release(c)
		b.forget(c.ID)
	}()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			conn.Close(websocket.StatusUnsupportedData, "protocol error")
			return
		}
		var req request
		if json.Unmarshal(data, &req) != nil || req.ID == "" || req.Method == "" {
			conn.Close(websocket.StatusProtocolError, "protocol error")
			return
		}
		b.dispatch(ctx, c, req) // in order: Rex requires it for block.event
	}
}

func (b *Bridge) dispatch(ctx context.Context, c *Client, req request) {
	out := map[string]any{"type": "response", "id": req.ID}
	var p P
	if len(req.Payload) > 0 {
		json.Unmarshal(req.Payload, &p)
	}
	h, ok := handlers[req.Method]
	if !ok {
		log.Printf("unknown method %s %s", req.Method, req.Payload)
		out["seq"] = c.currentSeq()
		out["error"] = newErr("method_not_found", "unknown method "+req.Method, 404)
		c.write(out)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// The response seq is the last event queued before the request started,
	// so events the handler itself emits are still applied by the client.
	out["seq"] = c.currentSeq()
	res, err := h(cctx, b, c, p)
	if os.Getenv("HERDR_IN_REX_TRACE") != "" {
		log.Printf("req client=%s %s %s %s -> seq=%v err=%v", c.ID, req.ID, req.Method, req.Payload, out["seq"], err)
	}
	if err != nil {
		if re, ok := err.(*rexError); ok {
			out["error"] = re
		} else {
			out["error"] = newErr("conflict", err.Error(), 400)
			log.Printf("%s: %v", req.Method, err)
		}
	} else {
		if res == nil {
			res = map[string]any{}
		}
		out["payload"] = res
	}
	if err := c.write(out); err != nil {
		log.Printf("write response: %v", err)
	}
}

// P is a request payload with typed accessors.
type P map[string]any

func (p P) str(k string) string  { s, _ := p[k].(string); return s }
func (p P) num(k string) float64 { f, _ := p[k].(float64); return f }
func (p P) obj(k string) P       { m, _ := p[k].(map[string]any); return P(m) }

func must(err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
