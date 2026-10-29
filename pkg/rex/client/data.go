package client

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// RejectError is a data handshake the server refused.
type RejectError struct{ Reason byte }

func (e *RejectError) Error() string {
	return fmt.Sprintf("data handshake rejected: reason %02x", e.Reason)
}

// Stream is a block's terminal stream.
type Stream struct {
	// Token resumes the stream; KeepMillis is how long the server keeps it
	// after a disconnect.
	Token      [16]byte
	KeepMillis uint32
	Nonce      uint64

	ws  *websocket.Conn
	dec *termstream.Decoder
}

// Terminal opens the terminal stream of a block. The connection must be
// attached to the block's session with session.attach or session.bootstrap.
func (c *Conn) Terminal(ctx context.Context, sessionID, blockID string) (*Stream, error) {
	var t struct {
		Ticket string `json:"ticket"`
	}
	err := c.Call(ctx, "data.connect",
		map[string]any{
			"session_id":  sessionID,
			"block_id":    blockID,
			"data_ref_id": "terminal",
			"subprotocol": rex.DataProtocol,
		}, &t)
	if err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(t.Ticket)
	if err != nil || len(raw) != 16 {
		return nil, fmt.Errorf("data.connect ticket %q is not 16 bytes of base64url", t.Ticket)
	}
	ws, _, err := websocket.Dial(ctx, c.base+"/api/data", &websocket.DialOptions{
		HTTPClient: c.http, Subprotocols: []string{rex.DataProtocol},
	})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(rex.MaxMessageBytes) // frames should be 64 KiB at most; allow more
	if err := ws.Write(ctx, websocket.MessageBinary, termstream.Hello([16]byte(raw))); err != nil {
		ws.CloseNow()
		return nil, err
	}
	_, msg, err := ws.Read(ctx)
	if err != nil {
		ws.CloseNow()
		return nil, closeErr(err)
	}
	if r, ok := termstream.ParseReject(msg); ok {
		ws.CloseNow()
		return nil, &RejectError{r}
	}
	s := &Stream{ws: ws}
	var ok bool
	if s.Token, s.KeepMillis, s.Nonce, ok = termstream.ParseWelcome(msg); !ok {
		ws.CloseNow()
		return nil, fmt.Errorf("data handshake: first message is % x", msg[:min(len(msg), 8)])
	}
	s.dec = termstream.NewDecoder(s.Nonce)
	return s, nil
}

// Read returns the next parts of the stream: program output, starting with
// the terminal snapshot, and Rex messages such as resize, theme and
// child_exited. At the end it returns a *CloseError.
func (s *Stream) Read(ctx context.Context) ([]termstream.Part, error) {
	for {
		_, msg, err := s.ws.Read(ctx)
		if err != nil {
			return nil, closeErr(err)
		}
		if p := s.dec.Decode(msg); len(p) > 0 {
			return p, nil
		}
	}
}

// Close ends the stream; the server does not keep it for resuming.
func (s *Stream) Close() error { return s.ws.Close(websocket.StatusNormalClosure, "") }
