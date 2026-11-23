package bridge

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// uploader drives the Rex side of a file drop: upload_begin on a control
// connection, then a rex.data.bytes.v1 stream per file.
type uploader struct {
	t       *testing.T
	b       *Bridge
	url     string
	session string
	block   string
}

func newUploader(t *testing.T, f *recordHerdr) *uploader {
	t.Helper()
	b := New(f.Socket)
	server := httptest.NewServer(b.Handler())
	t.Cleanup(server.Close)
	return &uploader{t: t, b: b, url: server.URL, session: b.IDs.Rex("session", "w1"), block: b.IDs.Rex("block", "w1:p1")}
}

// control opens a control connection attached to the session.
func (u *uploader) control() *client.Conn {
	u.t.Helper()
	conn, err := client.Dial(u.t.Context(), u.url, nil)
	if err != nil {
		u.t.Fatal(err)
	}
	u.t.Cleanup(func() { conn.Close() })
	if err := conn.Call(u.t.Context(), "session.attach", map[string]any{"session_id": u.session}, nil); err != nil {
		u.t.Fatal(err)
	}
	return conn
}

// begin reserves a file and returns its ticket, or the error.
func (u *uploader) begin(conn *client.Conn, args map[string]any) (string, error) {
	if args["ref"] == nil {
		args["ref"] = "upload:" + uuid.NewString()
	}
	var r struct {
		Ticket string `json:"ticket"`
	}
	err := conn.Call(u.t.Context(), "block.method", map[string]any{
		"session_id": u.session, "block_id": u.block, "method": "upload_begin", "args": args,
	}, &r)
	return r.Ticket, err
}

// stream opens the data connection for a ticket.
func (u *uploader) stream(ticket string) *websocket.Conn {
	u.t.Helper()
	ctx := u.t.Context()
	raw, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil {
		u.t.Fatal(err)
	}
	ws, _, err := websocket.Dial(ctx, u.url+"/api/data", &websocket.DialOptions{Subprotocols: []string{rex.DataBytesProtocol}})
	if err != nil {
		u.t.Fatal(err)
	}
	u.t.Cleanup(func() { ws.CloseNow() })
	if err := ws.Write(ctx, websocket.MessageBinary, termstream.Hello([16]byte(raw))); err != nil {
		u.t.Fatal(err)
	}
	if _, msg, err := ws.Read(ctx); err != nil {
		u.t.Fatal(err)
	} else if _, _, _, ok := termstream.ParseWelcome(msg); !ok {
		u.t.Fatal("no data welcome")
	}
	return ws
}

// closed waits for the bridge to close a stream and returns the close code.
func closed(ctx context.Context, ws *websocket.Conn) websocket.StatusCode {
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

// send writes data to a stream and waits for the bridge to close it.
func (u *uploader) send(ws *websocket.Conn, data string) websocket.StatusCode {
	u.t.Helper()
	if err := ws.Write(u.t.Context(), websocket.MessageBinary, []byte(data)); err != nil {
		u.t.Fatal(err)
	}
	return closed(u.t.Context(), ws)
}

// pastedTexts returns the texts the bridge typed into Herdr panes.
func pastedTexts(calls []herdrCall) []string {
	var out []string
	for _, c := range calls {
		if c.Method == "pane.send_text" {
			text, _ := c.Params["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

// Dropped files are typed once the whole batch has arrived, in index order,
// as one line of shell words separated by spaces, without a newline: a program with bracketed paste off gets no Enter, so
// the shell runs none of the paths.
func TestUploadTypesPathsOnOneLine(t *testing.T) {
	f := newRecordHerdr(t, nil)
	u := newUploader(t, f)
	store := &fakeStore{got: map[string][]byte{}}
	u.b.Uploads = store
	conn := u.control()
	var streams []*websocket.Conn
	for i, name := range []string{"a b.png", "c.png"} {
		ticket, err := u.begin(conn, map[string]any{"name": name, "size": 1, "batch": "drop", "index": i, "count": 2})
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, u.stream(ticket))
	}
	if code := u.send(streams[1], "z"); code != websocket.StatusNormalClosure {
		t.Fatalf("file 1 closed with %d", code)
	}
	if got := pastedTexts(f.inputs(0)); len(got) != 0 {
		t.Fatalf("half a batch typed %q", got)
	}
	if code := u.send(streams[0], "z"); code != websocket.StatusNormalClosure {
		t.Fatalf("file 0 closed with %d", code)
	}
	got := pastedTexts(f.inputs(0))
	want := `'/remote/tmp/herdrex-upload-x/a b.png' /remote/tmp/herdrex-upload-x/c.png`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("typed %q, want [%q]", got, want)
	}
	if string(store.got["a b.png"]) != "z" || string(store.got["c.png"]) != "z" {
		t.Errorf("stored %q", store.got)
	}
}

// A file the store cannot keep fails its stream with 1011, and its batch
// types nothing.
func TestUploadStoreFails(t *testing.T) {
	f := newRecordHerdr(t, nil)
	u := newUploader(t, f)
	u.b.Uploads = &fakeStore{got: map[string][]byte{}, err: errors.New("disk full")}
	ticket, err := u.begin(u.control(), map[string]any{"name": "a.png", "size": 1})
	if err != nil {
		t.Fatal(err)
	}
	if code := u.send(u.stream(ticket), "z"); code != websocket.StatusInternalError {
		t.Errorf("closed with %d, want 1011", code)
	}
	if got := pastedTexts(f.inputs(0)); len(got) != 0 {
		t.Errorf("a failed file typed %q", got)
	}
}

// A batch whose control connection closed is dropped: the bridge closes the
// streams still sending, types none of its files, and a new batch that takes
// its key stays reserved.
func TestUploadDroppedBatchStops(t *testing.T) {
	f := newRecordHerdr(t, nil)
	u := newUploader(t, f)
	u.b.Uploads = &fakeStore{got: map[string][]byte{}}
	a, b := u.control(), u.control()
	var streams []*websocket.Conn
	for i := range 2 {
		ticket, err := u.begin(a, map[string]any{"name": "f.png", "size": 2, "batch": "drop", "index": i, "count": 2})
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, u.stream(ticket))
	}
	if code := u.send(streams[0], "aa"); code != websocket.StatusNormalClosure {
		t.Fatalf("file 0 closed with %d", code)
	}
	if err := streams[1].Write(t.Context(), websocket.MessageBinary, []byte("b")); err != nil {
		t.Fatal(err)
	}
	a.Close()
	newer := map[string]any{"name": "g.png", "size": 0, "batch": "drop", "index": 0, "count": 3}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := u.begin(b, newer); err == nil {
			break // the bridge dropped a's batch, so b could take its key
		} else if time.Now().After(deadline) {
			t.Fatalf("a's batch was never dropped: %v", err)
		}
	}

	streams[1].Write(t.Context(), websocket.MessageBinary, []byte("b"))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if code := closed(ctx, streams[1]); ctx.Err() != nil || code == websocket.StatusNormalClosure {
		t.Errorf("a dropped batch's stream closed with %d (%v), want an error code", code, ctx.Err())
	}
	if got := pastedTexts(f.inputs(200 * time.Millisecond)); len(got) != 0 {
		t.Errorf("a dropped batch typed %q", got)
	}
	newer["ref"] = nil
	if _, err := u.begin(b, newer); err == nil {
		t.Error("the dropped batch's last file removed the newer batch with its key")
	}
}

// Files wait in memory until they are whole, and the listeners take anyone,
// so the bridge bounds what all connections together may reserve: the bytes
// of one largest file, and as many open batches as a batch has files.
// Dropping a connection's batches gives their share back.
func TestUploadLimits(t *testing.T) {
	f := newRecordHerdr(t, nil)
	u := newUploader(t, f)
	a, b := u.control(), u.control()
	tooLarge := func(err error) bool {
		var re *rex.Error
		return errors.As(err, &re) && re.Code == rex.CodeTooLarge
	}
	if _, err := u.begin(a, map[string]any{"name": "big", "size": maxUploadSize}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.begin(b, map[string]any{"name": "one", "size": 1}); !tooLarge(err) {
		t.Fatalf("a byte past the total: %v, want too_large", err)
	}
	a.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := u.begin(b, map[string]any{"name": "one", "size": 1})
		if err == nil {
			break
		}
		if !tooLarge(err) || time.Now().After(deadline) {
			t.Fatalf("after the big file's connection closed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 1; i < maxUploadCount; i++ {
		if _, err := u.begin(b, map[string]any{"name": "empty", "size": 0}); err != nil {
			t.Fatalf("batch %d: %v", i+1, err)
		}
	}
	if _, err := u.begin(b, map[string]any{"name": "empty", "size": 0}); !tooLarge(err) {
		t.Fatalf("batch %d: %v, want too_large", maxUploadCount+1, err)
	}
}
