// Package server serves Rex's control protocol rex.control.v1 and data
// protocol rex.data.terminal.v1 (docs/rex-api.md). It does the parts every
// Rex server shares: the hello and welcome, per-connection event seq and the
// seq of responses, attachments and the events they select, client presence,
// and data tickets and handshakes. Handlers registered with Handle decide
// what each method does.
//
// Requests on one connection run one at a time in arrival order, which meets
// the ordering rule of docs/rex-api.md 2.4 for every method. The server does
// no authentication, so it should listen on loopback only.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// Handler serves one method. payload is the request's raw payload, nil when
// absent. A *rex.Error is sent as is; any other error becomes a conflict.
// A nil result is sent as {}.
type Handler func(ctx context.Context, c *Client, payload json.RawMessage) (any, error)

type Server struct {
	// Data checks a data.connect request, without side effects. On success
	// it returns the function that serves the data connection that redeems
	// the ticket. When nil, data.connect fails with not_found.
	Data func(ctx context.Context, c *Client, r DataRequest) (func(ctx context.Context, st *DataStream), error)

	// OnDisconnect runs after a client is gone: its attachments are ended
	// and other clients told.
	OnDisconnect func(c *Client)

	// Trace logs every request and event.
	Trace bool

	mu       sync.Mutex
	handlers map[string]Handler
	clients  map[*Client]struct{}
	tickets  map[string]ticket
}

// New returns a server with the built-in methods: server.whoami,
// client.set_label, client.list, session.detach and data.connect.
func New() *Server {
	s := &Server{handlers: map[string]Handler{}, clients: map[*Client]struct{}{}, tickets: map[string]ticket{}}
	s.Handle("server.whoami", whoami)
	s.Handle("client.set_label", setLabel)
	s.Handle("client.list", s.clientList)
	s.Handle("session.detach", s.detach)
	s.Handle("data.connect", s.dataConnect)
	return s
}

// Handle registers a method, replacing any handler it had.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// ServeHTTP serves /api/control and /api/data.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/control":
		s.serveControl(w, r)
	case "/api/data":
		s.serveData(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{rex.ControlProtocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != rex.ControlProtocol {
		conn.Close(websocket.StatusProtocolError, "protocol error")
		return
	}
	conn.SetReadLimit(rex.MaxMessageBytes)
	c := &Client{ID: "client:" + uuid.NewString(), s: s, conn: conn, attached: map[string]time.Time{},
		principal: principal(r.RemoteAddr), connectedAt: time.Now()}
	ctx := r.Context()
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	typ, data, err := conn.Read(hctx)
	cancel()
	var hello rex.Hello
	if err != nil || typ != websocket.MessageText || json.Unmarshal(data, &hello) != nil || hello.Type != "hello" {
		conn.Close(websocket.StatusProtocolError, "protocol error")
		return
	}
	tok := make([]byte, 16)
	rand.Read(tok)
	c.write(map[string]any{"type": "welcome", "protocol": rex.ControlProtocol,
		"limits": map[string]any{"max_message_bytes": rex.MaxMessageBytes},
		"client": map[string]any{"id": c.ID, "reconnect_token": base64.RawURLEncoding.EncodeToString(tok),
			"resumed": false},
		"api_revision": rex.APIRevision, "api_server_revision": rex.APIRevision})
	c.info = hello.Client
	if c.info == nil {
		c.info = map[string]any{}
	}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	s.joined(c)
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		s.left(c)
		if s.OnDisconnect != nil {
			s.OnDisconnect(c)
		}
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
		var req rex.Request
		if json.Unmarshal(data, &req) != nil || req.ID == "" || req.Method == "" {
			conn.Close(websocket.StatusProtocolError, "protocol error")
			return
		}
		s.dispatch(ctx, c, req) // in order: Rex requires it for block.event and others
	}
}

func (s *Server) dispatch(ctx context.Context, c *Client, req rex.Request) {
	out := map[string]any{"type": "response", "id": req.ID}
	s.mu.Lock()
	h, ok := s.handlers[req.Method]
	s.mu.Unlock()
	if !ok {
		log.Printf("unknown method %s %s", req.Method, req.Payload)
		out["seq"] = c.currentSeq()
		out["error"] = rex.NewError(rex.CodeMethodNotFound, "unknown method "+req.Method, 404)
		c.write(out)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// The response seq is the last event queued before the request started,
	// so events the handler itself emits are still applied by the client.
	out["seq"] = c.currentSeq()
	res, err := h(cctx, c, req.Payload)
	if s.Trace {
		log.Printf("req client=%s %s %s %s -> seq=%v err=%v", c.ID, req.ID, req.Method, req.Payload, out["seq"], err)
	}
	if err != nil {
		if re, ok := err.(*rex.Error); ok {
			out["error"] = re
		} else {
			out["error"] = rex.NewError(rex.CodeConflict, err.Error(), 400)
			log.Printf("%s: %v", req.Method, err)
		}
	} else {
		if res == nil {
			res = map[string]any{}
		}
		out["payload"] = res
	}
	b, err := marshal(out)
	if err == nil && len(b) > rex.MaxMessageBytes {
		delete(out, "payload")
		out["error"] = rex.NewError(rex.CodeTooLarge, "result exceeds the message limit", 413)
		b, err = marshal(out)
	}
	if err == nil {
		err = c.send(b)
	}
	if err != nil {
		log.Printf("write response: %v", err)
	}
}

// Clients lists the connected clients, oldest first.
func (s *Server) Clients() []*Client {
	s.mu.Lock()
	cs := make([]*Client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	sort.Slice(cs, func(i, j int) bool { return cs[i].connectedAt.Before(cs[j].connectedAt) })
	return cs
}

// SessionEvent sends a session_event to the clients that get the session's
// events (docs/rex-api.md 2.5): those attached to it, and those attached to
// every session, which get block_event only with event_scope "all".
func (s *Server) SessionEvent(sid, typ string, payload any) {
	msg := map[string]any{"type": "session_event", "payload": map[string]any{
		"session_id": sid, "event": map[string]any{"type": typ, "payload": payload}}}
	for _, c := range s.Clients() {
		if typ == "block_event" && c.wantsBlockEvents(sid) || typ != "block_event" && c.wants(sid) {
			c.push(msg)
		}
	}
}

// Emit sends a top-level event to the clients that get it (docs/rex-api.md
// 2.5): session_destroyed to those attached to the session or to every
// session; keymap_changed and hosts_changed to everyone; client_joined and
// client_left to event_scope "all"; anything else, such as session_created
// and session_moved, to clients attached to every session.
func (s *Server) Emit(typ string, payload map[string]any) {
	msg := map[string]any{"type": typ, "payload": payload}
	sid, _ := payload["session_id"].(string)
	for _, c := range s.Clients() {
		var ok bool
		switch typ {
		case "session_destroyed":
			ok = c.wants(sid)
		case "keymap_changed", "hosts_changed":
			ok = true
		case "client_joined", "client_left":
			ok = c.wantsAll()
		default:
			ok = c.isAttachAll()
		}
		if ok {
			c.push(msg)
		}
	}
}

// keyRank orders envelope keys the way the real Rex server writes them:
// tag and routing fields first, the body last.
var keyRank = map[string]int{"type": -4, "id": -3, "seq": -2, "session_id": -1, "payload": 1, "error": 1}

// marshal works like json.Marshal, but orders map keys by keyRank and then
// by name, so messages match the real server's field order.
func marshal(v any) ([]byte, error) {
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
			vb, err := marshal(x[k])
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
			eb, err := marshal(e)
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
