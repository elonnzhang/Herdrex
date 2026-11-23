package server_test

import (
	"encoding/base64"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// ticket attaches c and asks for a terminal data ticket.
func ticket(t *testing.T, c *client.Conn) [16]byte {
	t.Helper()
	if err := c.Call(ctx(t), "session.attach", nil, nil); err != nil {
		t.Fatal(err)
	}
	var tk struct {
		Ticket string `json:"ticket"`
	}
	err := c.Call(ctx(t), "data.connect", map[string]any{
		"session_id": sid, "block_id": "block:b", "data_ref_id": "terminal", "subprotocol": rex.DataProtocol,
	}, &tk)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tk.Ticket)
	if err != nil || len(raw) != 16 {
		t.Fatalf("ticket %q: %v", tk.Ticket, err)
	}
	return [16]byte(raw)
}

// waitGone waits until the server no longer lists the client.
func waitGone(t *testing.T, s *rexsvr.Server, id string) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if !slices.ContainsFunc(s.Clients(), func(c *rexsvr.Client) bool { return c.ID == id }) {
			return
		}
	}
	t.Fatalf("client %s still connected", id)
}

// redeem sends a data HELLO with the ticket and returns the server's answer.
func redeem(t *testing.T, url string, tk [16]byte) []byte {
	t.Helper()
	ws, _, err := websocket.Dial(ctx(t), url+"/api/data", &websocket.DialOptions{Subprotocols: []string{rex.DataProtocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(ctx(t), websocket.MessageBinary, termstream.Hello(tk)); err != nil {
		t.Fatal(err)
	}
	_, msg, err := ws.Read(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// A data ticket needs its control connection to stay attached to the
// session (docs/rex-api.md 5.1): detaching or disconnecting revokes it.
func TestTicketRevoked(t *testing.T) {
	s, url := newServer(t)
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, c *client.Conn)
	}{
		{"detach", func(t *testing.T, c *client.Conn) {
			if err := c.Call(ctx(t), "session.detach", map[string]any{"session_id": sid}, nil); err != nil {
				t.Fatal(err)
			}
		}},
		{"disconnect", func(t *testing.T, c *client.Conn) {
			c.Close()
			waitGone(t, s, c.Welcome.Client.ID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, url, "")
			tk := ticket(t, c)
			tc.end(t, c)
			if r, ok := termstream.ParseReject(redeem(t, url, tk)); !ok || r != termstream.RejectUnknownTicket {
				t.Fatalf("redeemed a ticket after %s", tc.name)
			}
		})
	}
	c := dial(t, url, "")
	if _, _, _, ok := termstream.ParseWelcome(redeem(t, url, ticket(t, c))); !ok {
		t.Fatal("an attached client's ticket was refused")
	}
}
