package client_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// TestServer runs against a live server, such as the isolated Rex server of
// docs/TESTING.md: REX_TEST_SERVER=unix:///tmp/rexbridge/server.sock. It
// creates a session, reads the start of a terminal stream and destroys the
// session.
func TestServer(t *testing.T) {
	url := os.Getenv("REX_TEST_SERVER")
	if url == "" {
		t.Skip("set REX_TEST_SERVER to a Rex server URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, url, &client.Options{Kind: "cli", Name: "rex-go-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Logf("welcome %+v", c.Welcome)

	var re *rex.Error
	if err := c.Call(ctx, "no.such.method", nil, nil); !errors.As(err, &re) || re.Code != rex.CodeMethodNotFound {
		t.Fatalf("unknown method: %v", err)
	}
	var created struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	err = c.Call(ctx, "session.create", map[string]any{"label": "rex-go-test", "initial_windows": []any{
		map[string]any{"layout": map[string]any{"block": map[string]any{"flavor": rex.ShellFlavor}}}}}, &created)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { // destroy needs an attachment, which may not have happened yet
		id := map[string]any{"session_id": created.SessionID}
		c.Call(context.Background(), "session.attach", id, nil)
		if err := c.Call(context.Background(), "session.destroy", id, nil); err != nil {
			t.Errorf("session.destroy: %v", err)
		}
	}()
	if len(created.InitialWindows) == 0 || len(created.InitialWindows[0].BlockIDs) == 0 {
		t.Fatalf("session.create = %+v", created)
	}
	block := created.InitialWindows[0].BlockIDs[0]

	if _, err := c.Terminal(ctx, created.SessionID, block); !errors.As(err, &re) || re.Code != rex.CodeConflict {
		t.Fatalf("data.connect before attach: %v", err)
	}
	r, err := c.Do(ctx, "session.attach", map[string]any{"session_id": created.SessionID})
	if err != nil || r.Error != nil {
		t.Fatalf("attach = %+v %v", r, err)
	}
	var view rex.View
	if err := c.Call(ctx, "session.view", map[string]any{"session_id": created.SessionID}, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Windows) != 1 || view.Windows[0].Layers[0].Layout.BlockID != block {
		t.Fatalf("view = %+v", view)
	}

	st, err := c.Terminal(ctx, created.SessionID, block)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var out []byte
	sawResize := false
	for !sawResize {
		parts, err := st.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range parts {
			if p.Type == "resize" {
				sawResize = true
			} else if p.Type == "" && !sawResize {
				out = append(out, p.Output...)
			}
		}
	}
	if len(out) < 8 || string(out[:8]) != string(termstream.BlankSnapshot[:8]) {
		t.Fatalf("stream does not start with a snapshot: % x", out[:min(len(out), 16)])
	}

	// Events after the response's seq come from this connection's own call.
	for {
		select {
		case e := <-c.Events():
			if e.Seq > r.Seq {
				t.Logf("event %d %s", e.Seq, e.Type)
				return
			}
		case <-ctx.Done():
			t.Fatal("no event after attach")
		}
	}
}
