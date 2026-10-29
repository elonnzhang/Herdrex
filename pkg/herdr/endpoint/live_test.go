package endpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// TestLive runs against the Herdr server whose API socket is
// HERDR_TEST_SOCKET. It activates a surface, which claims the size of the
// tab it views and moves focus: point it only at an isolated server.
func TestLive(t *testing.T) {
	api := os.Getenv("HERDR_TEST_SOCKET")
	if api == "" {
		t.Skip("HERDR_TEST_SOCKET not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Dial(ctx, herdr.ClientSocket(api), Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Logf("server %s, boot %s", c.Welcome.ServerVersion, c.BootID())
	if n := len(c.Welcome.Methods); n != 40 {
		t.Errorf("%d methods, want 40: %v", n, c.Welcome.Methods)
	}
	for _, capability := range []string{"surface_interest", "health_check"} {
		if !slices.Contains(c.Welcome.Capabilities, capability) {
			t.Errorf("capabilities %v lack %s", c.Welcome.Capabilities, capability)
		}
	}
	if c.BootID() == "" {
		t.Error("BootID empty")
	}

	var je *jsonapi.Error
	if err := c.Call(ctx, "tab.create", nil, nil); !errors.As(err, &je) || je.Code != "surface_inactive" {
		t.Errorf("tab.create on an inactive connection: %v", err)
	}
	// Call refuses methods the welcome does not list with the code Herdr
	// itself answers for them.
	c.Welcome.Methods = append(slices.Clip(c.Welcome.Methods), "ping")
	if err := c.Call(ctx, "ping", nil, nil); !errors.As(err, &je) || je.Code != "unsupported_method" {
		t.Errorf("ping through the endpoint: %v", err)
	}
	c.Welcome.Methods = c.Welcome.Methods[:len(c.Welcome.Methods)-1]
	pingCtx, pingCancel := context.WithTimeout(ctx, time.Second)
	if err := c.HealthPing(pingCtx); err != nil {
		t.Errorf("HealthPing: %v", err)
	}
	pingCancel()

	if err := c.SetSurfaceActive(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Resize(80, 24, 8, 16, false); err != nil {
		t.Fatal(err)
	}
	var s *Surface
	deadline := time.After(5 * time.Second)
	for s == nil || s.Frame.Width != 80 || s.Frame.Height != 24 {
		select {
		case m, ok := <-c.Events():
			if !ok {
				t.Fatalf("events closed: %v", c.Err())
			}
			if m, ok := m.(*Surface); ok {
				s = m
			}
		case <-deadline:
			t.Fatalf("no 80×24 surface within 5 s; last %+v", s)
		}
	}
	if len(s.Panes) == 0 {
		t.Fatal("surface has no panes")
	}
	pane := s.Panes[0]
	for _, p := range s.Panes {
		if p.Focused {
			pane = p
		}
	}
	t.Logf("surface %d: %d panes, typing into %s at %+v", s.SurfaceRevision, len(s.Panes), pane.PaneID, pane.InnerRect)

	// A fresh word each run, so output left by an earlier run does not count.
	word := fmt.Sprintf("ep-1-%d", time.Now().UnixMicro()%1e6)
	if err := c.PaneInput(pane.PaneID, TextCommit("echo "+word), Key{Code: KeyEnter}); err != nil {
		t.Fatal(err)
	}
	var patches, rejected int
	deadline = time.After(5 * time.Second)
	for !hasOutput(s, pane.PaneID, word) {
		select {
		case m, ok := <-c.Events():
			if !ok {
				t.Fatalf("events closed: %v", c.Err())
			}
			switch m := m.(type) {
			case *Surface:
				s = m
			case *SurfacePatch:
				if err := s.Apply(m); err != nil {
					rejected++
					t.Logf("patch: %v", err)
				} else {
					patches++
				}
			}
		case <-deadline:
			t.Fatalf("no row reads %s within 5 s:\n%s", word, strings.Join(rows(s), "\n"))
		}
	}
	t.Logf("%s seen after %d patches applied, %d rejected", word, patches, rejected)

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var pong struct {
		Type     string `json:"type"`
		Protocol uint32 `json:"protocol"`
	}
	jc := jsonapi.Client{Socket: api}
	if err := jc.Call(ctx, "ping", nil, &pong); err != nil || pong.Type != "pong" {
		t.Errorf("ping after Close: %+v %v", pong, err)
	}
}

// rows returns the surface's rows as text.
func rows(s *Surface) []string {
	w := int(s.Frame.Width)
	var out []string
	for y := range int(s.Frame.Height) {
		var b strings.Builder
		for _, cell := range s.Frame.Cells[y*w : (y+1)*w] {
			b.WriteString(cell.Symbol)
		}
		out = append(out, b.String())
	}
	return out
}

// hasOutput reports whether a row of the pane's terminal reads text alone,
// as the command's output does, and not only inside the typed command.
func hasOutput(s *Surface, paneID, text string) bool {
	i := slices.IndexFunc(s.Panes, func(p SurfacePane) bool { return p.PaneID == paneID })
	if i < 0 {
		return false
	}
	r := s.Panes[i].InnerRect
	for _, row := range rows(s)[r.Y : r.Y+r.Height] {
		cells := []rune(row) // one symbol per cell in this test's ASCII rows
		if int(r.X+r.Width) <= len(cells) && strings.TrimSpace(string(cells[r.X:r.X+r.Width])) == text {
			return true
		}
	}
	return false
}
