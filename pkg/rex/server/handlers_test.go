package server_test

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// A built-in method whose payload does not decode fails with bad_request and
// changes nothing.
func TestBuiltinsRejectBadPayload(t *testing.T) {
	_, url := newServer(t)
	c := dial(t, url, "keep")
	if err := c.Call(ctx(t), "session.attach", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method  string
		payload any
	}{
		{"client.set_label", map[string]any{"label": 5}},
		{"client.list", map[string]any{"session_id": 5}},
		{"session.detach", map[string]any{"session_id": 5}},
		{"data.connect", map[string]any{"session_id": sid, "block_id": 5, "data_ref_id": "terminal"}},
	} {
		var re *rex.Error
		if err := c.Call(ctx(t), tc.method, tc.payload, nil); !errors.As(err, &re) || re.Code != rex.CodeBadRequest || re.Status != 400 {
			t.Errorf("%s %v: %v, want bad_request", tc.method, tc.payload, err)
		}
	}
	var l struct {
		Clients []struct {
			Info map[string]any `json:"info"`
		} `json:"clients"`
	}
	if err := c.Call(ctx(t), "client.list", nil, &l); err != nil || len(l.Clients) != 1 || l.Clients[0].Info["label"] != "keep" {
		t.Fatalf("client.list = %+v %v, want the label kept", l, err)
	}
}

// An unknown method is logged by name and payload size, never by payload,
// which can carry keystrokes or pasted text.
func TestUnknownMethodLogOmitsPayload(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	_, url := newServer(t)
	c := dial(t, url, "")
	var re *rex.Error
	if err := c.Call(ctx(t), "no.such", map[string]any{"text": "hunter2"}, nil); !errors.As(err, &re) || re.Code != rex.CodeMethodNotFound {
		t.Fatalf("unknown method: %v", err)
	}
	if got := buf.String(); strings.Contains(got, "hunter2") || !strings.Contains(got, "no.such") {
		t.Fatalf("log = %q, want the method name without the payload", got)
	}
}
