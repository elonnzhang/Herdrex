package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// uploadServer has a method that mints an upload ticket and hands the bytes
// it receives to a channel.
func uploadServer(t *testing.T) (string, <-chan []byte) {
	t.Helper()
	s, url := newServer(t)
	got := make(chan []byte, 4)
	s.Handle("reserve", func(ctx context.Context, c *rexsvr.Client, p json.RawMessage) (any, error) {
		ticket, ttl, err := s.UploadTicket(c, rex.DataBytesProtocol, func(ctx context.Context, st *rexsvr.UploadStream) {
			var all []byte
			for {
				msg, err := st.Read(ctx)
				if err != nil {
					got <- all
					return
				}
				all = append(all, msg...)
			}
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"ticket": ticket, "expires_in_ms": ttl}, nil
	})
	return url, got
}

// The bytes stream: a HELLO with the ticket, a WELCOME, then the client's
// messages, which the serving function reads in order.
func TestUploadStream(t *testing.T) {
	url, got := uploadServer(t)
	c := dial(t, url, "")
	var tk struct {
		Ticket      string `json:"ticket"`
		ExpiresInMS int    `json:"expires_in_ms"`
	}
	if err := c.Call(ctx(t), "reserve", nil, &tk); err != nil {
		t.Fatal(err)
	}
	if tk.ExpiresInMS <= 0 {
		t.Fatalf("expires_in_ms = %d", tk.ExpiresInMS)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tk.Ticket)
	if err != nil || len(raw) != 16 {
		t.Fatalf("ticket %q: %v", tk.Ticket, err)
	}
	conn := dialData(t, url, rex.DataBytesProtocol, raw)
	mustWrite(t, conn, []byte("abc"))
	mustWrite(t, conn, []byte("de"))
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-got:
		if string(b) != "abcde" {
			t.Fatalf("server read %q", b)
		}
	case <-ctx(t).Done():
		t.Fatal("the serving function never got the stream")
	}
}

// A ticket names its subprotocol: a terminal connection cannot redeem one.
func TestUploadTicketSubprotocol(t *testing.T) {
	url, _ := uploadServer(t)
	c := dial(t, url, "")
	var tk struct {
		Ticket string `json:"ticket"`
	}
	if err := c.Call(ctx(t), "reserve", nil, &tk); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tk.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	conn := dialRaw(t, url, rex.DataProtocol)
	mustWrite(t, conn, termstream.Hello([16]byte(raw)))
	typ, msg, err := conn.Read(ctx(t))
	if err != nil || typ != websocket.MessageBinary {
		t.Fatalf("read %v %v", typ, err)
	}
	if r, ok := termstream.ParseReject(msg); !ok || r != termstream.RejectSubprotocol {
		t.Fatalf("answer %x", msg)
	}
}

func dialData(t *testing.T, url, sub string, ticket []byte) *websocket.Conn {
	t.Helper()
	conn := dialRaw(t, url, sub)
	mustWrite(t, conn, termstream.Hello([16]byte(ticket)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, msg, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary {
		t.Fatalf("handshake: %v %v", typ, err)
	}
	if _, _, _, ok := termstream.ParseWelcome(msg); !ok {
		t.Fatalf("handshake answer %x", msg)
	}
	return conn
}

func dialRaw(t *testing.T, url, sub string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+url[len("http"):]+"/api/data", &websocket.DialOptions{Subprotocols: []string{sub}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func mustWrite(t *testing.T, conn *websocket.Conn, b []byte) {
	t.Helper()
	if err := conn.Write(context.Background(), websocket.MessageBinary, b); err != nil {
		t.Fatal(err)
	}
}
