package bridge

import (
	"context"
	"log"
	"strings"

	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// dataSource answers data.connect for a block's terminal. The server checks
// the attachment and does the handshake; the stream starts with a blank
// snapshot, since Herdr's frames repaint the screen anyway.
func (b *Bridge) dataSource(ctx context.Context, c *rexsvr.Client, r rexsvr.DataRequest) (func(context.Context, *rexsvr.DataStream), error) {
	if r.DataRefID != "terminal" {
		return nil, rex.NewError("not_found", "block has no data source "+r.DataRefID, 404)
	}
	pane, err := b.herdrID(r.BlockID, "block")
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, st *rexsvr.DataStream) {
		if st.Send(ctx, termstream.BlankSnapshot) != nil {
			return
		}
		log.Printf("data stream open: pane %s", pane)
		b.stream(ctx, pane, st)
		log.Printf("data stream closed: pane %s", pane)
	}, nil
}

// stream forwards the pane's terminal frames from its hub, telling the client
// the size before the first frame and whenever it changes, and whether the
// program reports the mouse. Bracketed paste is turned on once, before the
// first frame.
func (b *Bridge) stream(ctx context.Context, pane string, st *rexsvr.DataStream) {
	block := b.IDs.Rex("block", pane)
	send := func(p []byte) error { return st.Send(ctx, p) }
	h, s, err := b.join(ctx, pane, block)
	if err != nil {
		log.Printf("terminal %s: %v", pane, err)
		if isPaneGone(err) || strings.Contains(err.Error(), "not found") {
			st.Close(rex.CloseBlockClosed, "block closed")
		} else {
			st.Close(rex.CloseProviderFailed, "data provider failed")
		}
		return
	}
	defer b.leave(h, s)
	var w, ht uint16
	themed, mouse := false, false // mouse: the client was told the program reports it
	syncMouse := func() error {
		if on := h.reportsMouse(); on != mouse {
			mouse = on
			return send(mouseModes[on])
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-s.out:
			if !ok {
				st.Close(rex.CloseReconnectRequired, "data reconnect required")
				return
			}
			if m.theme != nil {
				if w != 0 && send(termstream.Theme(st.Nonce, m.theme)) != nil {
					return
				}
				continue
			}
			if m.mouse {
				if w != 0 && syncMouse() != nil {
					return
				}
				continue
			}
			if m.frame == nil {
				if m.exited { // Herdr reports no exit status
					send(termstream.ChildExited(st.Nonce, 0))
				}
				st.Close(int(m.code), m.why)
				return
			}
			f := m.frame
			if f.Width != w || f.Height != ht {
				w, ht = f.Width, f.Height
				cs := h.current()
				if send(termstream.Resize(st.Nonce, int(w), int(ht), int(cs.CellWidthPx), int(cs.CellHeightPx), b.owner(block))) != nil {
					return
				}
			}
			if !themed { // the theme follows the first resize
				themed = true
				if t := b.theme(block); t != nil && send(termstream.Theme(st.Nonce, t)) != nil {
					return
				}
				if send(bracketedPaste) != nil {
					return
				}
			}
			if syncMouse() != nil || send(f.Bytes) != nil {
				return
			}
		}
	}
}
