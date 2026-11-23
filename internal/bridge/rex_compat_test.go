package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

func rexTestClient(t *testing.T, bridge *Bridge) (*client.Conn, string) {
	t.Helper()
	server := httptest.NewServer(bridge.Handler())
	t.Cleanup(server.Close)
	conn, err := client.Dial(t.Context(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, server.URL
}

func TestRexHelp(t *testing.T) {
	conn, url := rexTestClient(t, New("/tmp/hx-no-herdr.sock"))
	type method struct {
		Name       string          `json:"name"`
		ArgsSchema json.RawMessage `json:"args_schema"`
	}
	type document struct {
		Methods    []method `json:"methods"`
		BlockTypes []struct {
			Creator string   `json:"creator"`
			Methods []method `json:"methods"`
		} `json:"block_types"`
		HTTP map[string]string `json:"http"`
	}
	var control, httpHelp document
	if err := conn.Call(t.Context(), "help.all", nil, &control); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/api/help", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(&httpHelp); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || httpHelp.HTTP["help_path"] != "/api/help" || control.HTTP != nil || len(control.Methods) != len(httpHelp.Methods) {
		t.Fatal("HTTP and control discovery disagree")
	}
	for _, help := range []document{control, httpHelp} {
		if !slices.ContainsFunc(help.Methods, func(entry method) bool { return entry.Name == "help.all" }) ||
			slices.ContainsFunc(help.Methods, func(entry method) bool { return entry.Name == "server.stop" }) {
			t.Fatal("discovery did not match the registered control methods")
		}
		if len(help.BlockTypes) != 1 || help.BlockTypes[0].Creator != rex.TerminalCreator {
			t.Fatal("discovery did not advertise exactly the terminal creator")
		}
		methods := help.BlockTypes[0].Methods
		if !slices.ContainsFunc(methods, func(entry method) bool { return entry.Name == "upload_begin" && len(entry.ArgsSchema) > 0 }) ||
			slices.ContainsFunc(methods, func(entry method) bool { return entry.Name == "download_begin" }) {
			t.Fatal("discovery did not match the registered terminal methods")
		}
	}
	if binary := os.Getenv("REX_TEST_BIN"); binary != "" {
		output, err := exec.CommandContext(t.Context(), binary, "--autostart=false", "-S", url, "api", "list", "--json").Output()
		var fromCLI document
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(output, &fromCLI); err != nil || len(fromCLI.Methods) != len(control.Methods) {
			t.Fatalf("rex api list returned %d methods, want %d (decode error: %v)", len(fromCLI.Methods), len(control.Methods), err)
		}
	}
}

func TestRexBootstrapAndTerminalArgs(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hxapi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "api.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	texts := make(chan string, 4)
	go func() {
		for {
			peer, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer peer.Close()
				var request struct {
					Method string `json:"method"`
					Params struct {
						Text string `json:"text"`
					} `json:"params"`
				}
				if json.NewDecoder(peer).Decode(&request) != nil {
					return
				}
				var result string
				switch request.Method {
				case "session.snapshot":
					result = `{"type":"session_snapshot","snapshot":{"workspaces":[{"workspace_id":"w1","active_tab_id":"w1:t1"}],"tabs":[{"workspace_id":"w1","tab_id":"w1:t1"}],"panes":[{"workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1"}]}}`
				case "layout.export":
					result = `{"type":"layout_export","layout":{"focused_pane_id":"w1:p1","root":{"type":"pane","pane_id":"w1:p1"}}}`
				case "pane.send_text":
					texts <- request.Params.Text
					result = `{"type":"pane_text_sent"}`
				default:
					result = `{}`
				}
				fmt.Fprintf(peer, "{\"result\":%s}\n", result)
			}()
		}
	}()
	bridge := New(socket)
	bridge.srv.Data = func(ctx context.Context, caller *rexsvr.Client, request rexsvr.DataRequest) (func(context.Context, *rexsvr.DataStream), error) {
		return func(ctx context.Context, stream *rexsvr.DataStream) {
			stream.Send(ctx, []byte(request.BlockID))
		}, nil
	}
	conn, url := rexTestClient(t, bridge)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sessionID := bridge.IDs.Rex("session", "w1")
	blockID := bridge.IDs.Rex("block", "w1:p1")
	request := map[string]any{"creator_name": rex.TerminalCreator, "data_ref_id": "terminal", "subprotocol": rex.DataProtocol}
	args := map[string]any{"event_scope": "session", "data_tickets": []any{request, request}}
	for _, sessions := range [][]string{nil, {"session:unknown", sessionID, sessionID}, {}} {
		delete(args, "attach_session_ids")
		if sessions != nil {
			args["attach_session_ids"] = sessions
		}
		var result struct {
			Tickets []bootstrapTicket `json:"data_tickets"`
		}
		if err := conn.Call(ctx, "session.bootstrap", args, &result); err != nil {
			t.Fatal(err)
		}
		if sessions != nil && len(sessions) == 0 {
			if len(result.Tickets) != 0 {
				t.Fatal("explicitly empty attach list received a ticket")
			}
			continue
		}
		if len(result.Tickets) != 1 {
			t.Fatalf("got %d bootstrap tickets, want 1", len(result.Tickets))
		}
		ticket := result.Tickets[0]
		raw, err := base64.RawURLEncoding.DecodeString(ticket.Ticket)
		if err != nil || len(raw) != 16 || ticket.ExpiresInMS <= 0 || ticket.SessionID != sessionID || ticket.BlockID != blockID || ticket.Subprotocol != rex.DataProtocol || ticket.DataRefID != "terminal" {
			t.Fatal("invalid bootstrap ticket")
		}
		if sessions == nil {
			stream, _, err := websocket.Dial(ctx, url+"/api/data", &websocket.DialOptions{Subprotocols: []string{rex.DataProtocol}})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.CloseNow()
			if err := stream.Write(ctx, websocket.MessageBinary, termstream.Hello([16]byte(raw))); err != nil {
				t.Fatal(err)
			}
			_, message, err := stream.Read(ctx)
			if _, _, _, valid := termstream.ParseWelcome(message); err != nil || !valid {
				t.Fatalf("invalid data welcome: %v", err)
			}
			if _, message, err := stream.Read(ctx); err != nil || string(message) != blockID {
				t.Fatalf("ticket did not open the requested stream: %v", err)
			}
		}
	}
	for _, text := range []string{"nested", "flat"} {
		payload := map[string]any{"session_id": sessionID, "block_id": blockID}
		if text == "nested" {
			payload["args"] = map[string]string{"data": text}
		} else {
			payload["data"] = text
		}
		var result struct {
			Bytes int `json:"bytes_written"`
		}
		if err := conn.Call(ctx, rex.TerminalCreator+".write", payload, &result); err != nil {
			t.Fatal(err)
		}
		if result.Bytes != len(text) {
			t.Fatalf("write did not pass %q to Herdr", text)
		}
		select {
		case received := <-texts:
			if received != text {
				t.Fatalf("Herdr received %q, want %q", received, text)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
