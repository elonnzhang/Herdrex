package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

const (
	ticketTTL    = 60 * time.Second
	streamKeepMS = 60000
	// maxUploadFrame is the most one frame of an upload stream carries. Rex
	// appends the frames, so this only bounds a single message.
	maxUploadFrame = 32 << 20
)

// DataRequest is a data.connect payload.
type DataRequest struct {
	SessionID   string `json:"session_id"`
	BlockID     string `json:"block_id"`
	DataRefID   string `json:"data_ref_id"`
	Subprotocol string `json:"subprotocol"`
}

type ticket struct {
	client  *Client
	req     DataRequest
	sub     string // the subprotocol the connection must use; "" for data.connect's
	serve   func(ctx context.Context, st *DataStream)
	upload  func(ctx context.Context, st *UploadStream)
	expires time.Time
}

// DataStream is an accepted data connection, after the WELCOME. Whatever is
// sent is the stream; the server closes the connection when the serving
// function returns, unless it closed it first.
type DataStream struct {
	Client  *Client // the control connection that asked for the ticket
	Request DataRequest
	Nonce   uint64 // the nonce of the stream's Rex messages

	conn *websocket.Conn
	mu   sync.Mutex
}

// Send writes the next bytes of the stream, in frames of at most 64 KiB.
func (st *DataStream) Send(ctx context.Context, p []byte) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	for len(p) > 0 {
		n := min(len(p), termstream.MaxFrame)
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := st.conn.Write(wctx, websocket.MessageBinary, p[:n])
		cancel()
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// Close ends the connection with a close code, such as rex.CloseBlockClosed.
func (st *DataStream) Close(code int, reason string) {
	st.conn.Close(websocket.StatusCode(code), reason)
}

// UploadStream is an accepted data connection that the client writes to,
// after the WELCOME. The handshake is the one of data.connect: a HELLO with
// the ticket that UploadTicket issued, then a WELCOME. Read returns the next
// message the client sent; the frames carry the bytes in order, so the
// caller appends them until the size it expects has arrived. It returns an
// error once the client closed the connection.
type UploadStream struct {
	Client  *Client // the control connection that asked for the ticket
	Request DataRequest
	Nonce   uint64 // the nonce of the stream's Rex messages

	conn *websocket.Conn
}

// Read returns the next message the client sent, or the error that ended the
// connection.
func (st *UploadStream) Read(ctx context.Context) ([]byte, error) {
	_, data, err := st.conn.Read(ctx)
	return data, err
}

// Close ends the connection with a close code, such as 1000 once the bytes
// have all arrived.
func (st *UploadStream) Close(code int, reason string) {
	st.conn.Close(websocket.StatusCode(code), reason)
}

// UploadTicket issues a ticket for a data connection that carries bytes to
// the server, used by methods such as com.superlogical.terminal.upload_begin.
// The connection must use subprotocol sub; serve runs after the handshake
// returns, and the server closes the connection when it returns. The ticket
// works once, within the same time as data.connect's.
func (s *Server) UploadTicket(c *Client, sub string, serve func(ctx context.Context, st *UploadStream)) (string, int, error) {
	if sub == "" {
		return "", 0, errors.New("upload ticket needs a subprotocol")
	}
	t := s.newTicket(c, DataRequest{Subprotocol: sub}, nil, serve, sub)
	return t.ticket, t.expiresInMS, nil
}

type issued struct {
	ticket      string
	expiresInMS int
}

func (s *Server) newTicket(c *Client, r DataRequest, serve func(context.Context, *DataStream), upload func(context.Context, *UploadStream), sub string) issued {
	var raw [16]byte
	rand.Read(raw[:])
	s.mu.Lock()
	for k, t := range s.tickets { // drop expired tickets
		if time.Now().After(t.expires) {
			delete(s.tickets, k)
		}
	}
	s.tickets[string(raw[:])] = ticket{
		client: c, req: r, sub: sub, serve: serve, upload: upload,
		expires: time.Now().Add(ticketTTL),
	}
	s.mu.Unlock()
	return issued{ticket: base64.RawURLEncoding.EncodeToString(raw[:]), expiresInMS: int(ticketTTL.Milliseconds())}
}

// dataConnect issues a ticket for a data connection to a block. The caller
// must be attached to the block's session.
func (s *Server) dataConnect(ctx context.Context, c *Client, payload json.RawMessage) (any, error) {
	var r DataRequest
	json.Unmarshal(payload, &r)
	if !c.Attached(r.SessionID) {
		return nil, rex.NewError(rex.CodeConflict, "connection is not attached to this session", 409)
	}
	if s.Data == nil {
		return nil, rex.NewError(rex.CodeNotFound, "block has no data source "+r.DataRefID, 404)
	}
	serve, err := s.Data(ctx, c, r)
	if err != nil {
		return nil, err
	}
	if r.Subprotocol != "" && r.Subprotocol != rex.DataProtocol {
		return nil, rex.NewError(rex.CodeBadRequest, "unsupported subprotocol "+r.Subprotocol, 400)
	}
	t := s.newTicket(c, r, serve, nil, "")
	return map[string]any{"ticket": t.ticket, "expires_in_ms": t.expiresInMS}, nil
}

func (s *Server) redeem(t [16]byte) (ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tk, ok := s.tickets[string(t[:])]
	delete(s.tickets, string(t[:])) // a ticket works once, even if refused
	if !ok || time.Now().After(tk.expires) {
		return ticket{}, false
	}
	return tk, true
}

func reject(conn *websocket.Conn, reason byte, code websocket.StatusCode) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn.Write(ctx, websocket.MessageBinary, termstream.Reject(reason))
	conn.Close(code, "data handshake rejected")
}

func (s *Server) serveData(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{rex.DataProtocol, rex.DataBytesProtocol}, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	hctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	typ, msg, err := conn.Read(hctx)
	cancel()
	hello, ok := termstream.ParseHello(msg)
	if err != nil || typ != websocket.MessageBinary || !ok {
		reject(conn, termstream.RejectMalformed, websocket.StatusProtocolError)
		return
	}
	tk, ok := s.redeem(hello)
	if !ok {
		reject(conn, termstream.RejectUnknownTicket, websocket.StatusPolicyViolation)
		return
	}
	if tk.sub != "" && conn.Subprotocol() != tk.sub {
		reject(conn, termstream.RejectSubprotocol, websocket.StatusProtocolError)
		return
	}
	var token [16]byte
	rand.Read(token[:])
	var nb [8]byte
	rand.Read(nb[:])
	nonce := binary.LittleEndian.Uint64(nb[:]) | 1 // never zero
	if tk.upload != nil {
		conn.SetReadLimit(maxUploadFrame)
		st := &UploadStream{Client: tk.client, Request: tk.req, Nonce: nonce, conn: conn}
		if conn.Write(r.Context(), websocket.MessageBinary, termstream.Welcome(token, streamKeepMS, nonce)) != nil {
			return
		}
		tk.upload(r.Context(), st)
		return
	}
	st := &DataStream{
		Client: tk.client, Request: tk.req,
		Nonce: nonce,
		conn:  conn,
	}
	ctx := conn.CloseRead(r.Context()) // answers pings, ignores client messages
	if st.Send(ctx, termstream.Welcome(token, streamKeepMS, st.Nonce)) != nil {
		return
	}
	tk.serve(ctx, st)
}
