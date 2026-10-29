package cli

import (
	"strings"
	"testing"
)

func TestSessions(t *testing.T) {
	f := newFake(t)
	c := f.cli()
	f.answer("session list --json", `{"sessions":[`+
		`{"default":true,"name":"default","running":true,"session_dir":"/x/herdr","socket_path":"/x/herdr/herdr.sock"},`+
		`{"connection_error":"Permission denied (os error 13)","default":false,"name":"work","running":false,`+
		`"session_dir":"/x/herdr/sessions/work","socket_path":"/x/herdr/sessions/work/herdr.sock"}]}`+"\n", "", 0)
	got, err := c.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []Session{
		{Name: "default", Default: true, Running: true, SessionDir: "/x/herdr", SocketPath: "/x/herdr/herdr.sock"},
		{
			Name: "work", ConnectionError: "Permission denied (os error 13)",
			SessionDir: "/x/herdr/sessions/work", SocketPath: "/x/herdr/sessions/work/herdr.sock",
		},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Sessions = %+v\nwant %+v", got, want)
	}
	f.answer("session list --json", "", "usage: herdr session list [--json]\n", 2)
	_, err = c.Sessions(t.Context())
	if e, ok := err.(*Error); !ok || e.ExitCode != 2 {
		t.Errorf("usage error: err = %v", err)
	}
}

func TestStopDeleteSession(t *testing.T) {
	f := newFake(t)
	c := f.cli()
	info := `{"default":false,"name":"-h","running":false,"session_dir":"/x/sessions/-h","socket_path":"/x/sessions/-h/herdr.sock"}`
	f.answer("session stop --json -- -h", `{"stopped":true,"session":`+info+"}\n", "", 0)
	f.answer("session delete --json -- -h", `{"deleted":true,"session":`+info+"}\n", "", 0)
	want := Session{Name: "-h", SessionDir: "/x/sessions/-h", SocketPath: "/x/sessions/-h/herdr.sock"}
	s, err := c.StopSession(t.Context(), "-h")
	if err != nil || *s != want {
		t.Errorf("StopSession = %+v, %v", s, err)
	}
	s, err = c.DeleteSession(t.Context(), "-h")
	if err != nil || *s != want {
		t.Errorf("DeleteSession = %+v, %v", s, err)
	}
	args := strings.Join(f.log("args"), "\n")
	if args != "[session][stop][--json][--][-h]\n[session][delete][--json][--][-h]" {
		t.Errorf("args = %q", args)
	}

	f.answer("session stop --json -- nope", "",
		`{"error":{"code":"session_stop_failed","message":"session nope is not running"}}`+"\n", 1)
	_, err = c.StopSession(t.Context(), "nope")
	if e, ok := err.(*Error); !ok || e.Code != "session_stop_failed" || e.ExitCode != 1 {
		t.Errorf("stop error: %v", err)
	}
	f.answer("session delete --json -- default", "",
		`{"error":{"code":"session_delete_failed","message":"deleting the default session is not supported"}}`+"\n", 1)
	_, err = c.DeleteSession(t.Context(), "default")
	if e, ok := err.(*Error); !ok || e.Code != "session_delete_failed" {
		t.Errorf("delete error: %v", err)
	}
	f.answer("session delete --json -- odd", "{\"deleted\":true}\n", "", 0)
	if s, err := c.DeleteSession(t.Context(), "odd"); err == nil {
		t.Errorf("no session in output: got %+v", s)
	}
}
