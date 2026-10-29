package bridge

import (
	"context"

	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// session.list_clients. The server keeps the clients and serves client.list,
// server.whoami and client.set_label; this one also needs the session to
// exist in Herdr.
func hListClients(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	sid := p.str("session_id")
	if _, err := b.herdrID(sid, "session"); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, o := range b.srv.Clients() {
		if o.Attached(sid) {
			out = append(out, o.Describe(sid))
		}
	}
	return map[string]any{"clients": out}, nil
}
