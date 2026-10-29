package termstream

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestHandshake(t *testing.T) {
	var ticket [16]byte
	copy(ticket[:], "0123456789abcdef")
	got, ok := ParseHello(Hello(ticket))
	if !ok || got != ticket {
		t.Fatalf("hello round trip = %x %v", got, ok)
	}
	w := Welcome(ticket, 60000, 42)
	if len(w) != 32 {
		t.Fatalf("welcome is %d bytes", len(w))
	}
	tok, keep, nonce, ok := ParseWelcome(w)
	if !ok || tok != ticket || keep != 60000 || nonce != 42 {
		t.Fatalf("welcome round trip = %x %d %d %v", tok, keep, nonce, ok)
	}
	if r, ok := ParseReject(Reject(RejectUnknownTicket)); !ok || r != RejectUnknownTicket {
		t.Fatalf("reject round trip = %d %v", r, ok)
	}
	if _, _, _, ok := ParseWelcome(Reject(1)); ok {
		t.Fatal("REJECT parsed as WELCOME")
	}
}

// stream is a snapshot, output, a resize, an APC with another nonce, more
// output and child_exited.
func stream() []byte {
	var s []byte
	s = append(s, BlankSnapshot...)
	s = append(s, "hello\r\n"...)
	s = append(s, Resize(7, 100, 30, 8, 16, "client:x")...)
	s = append(s, "\x1b_rex1;8;{\"type\":\"resize\"}\x1b\\"...)
	s = append(s, "\x1b_other\x1b\\bye"...)
	s = append(s, ChildExited(7, 0)...)
	return s
}

// collect joins adjacent output parts, since where output splits depends on
// the frames.
func collect(parts []Part) []Part {
	var out []Part
	for _, p := range parts {
		if p.Type == "" && len(out) > 0 && out[len(out)-1].Type == "" {
			out[len(out)-1].Output = append(out[len(out)-1].Output, p.Output...)
			continue
		}
		out = append(out, p)
	}
	return out
}

func TestDecoder(t *testing.T) {
	s := stream()
	want := []string{"output", "resize", "output", "child_exited"}
	// Split the stream at every position, as frame boundaries carry no meaning.
	for cut := 0; cut <= len(s); cut++ {
		d := NewDecoder(7)
		parts := collect(append(d.Decode(s[:cut]), d.Decode(s[cut:])...))
		if len(parts) != len(want) {
			t.Fatalf("cut %d: %d parts, want %d: %+v", cut, len(parts), len(want), parts)
		}
		for i, p := range parts {
			kind := p.Type
			if kind == "" {
				kind = "output"
			}
			if kind != want[i] {
				t.Fatalf("cut %d: part %d is %s, want %s", cut, i, kind, want[i])
			}
		}
		if !bytes.HasPrefix(parts[0].Output, BlankSnapshot) || !bytes.HasSuffix(parts[0].Output, []byte("hello\r\n")) {
			t.Fatalf("cut %d: first output is wrong", cut)
		}
		other := "\x1b_rex1;8;{\"type\":\"resize\"}\x1b\\\x1b_other\x1b\\bye"
		if string(parts[2].Output) != other {
			t.Fatalf("cut %d: middle output = %q", cut, parts[2].Output)
		}
		var r struct {
			Cols, Rows int
			Owner      string
		}
		if err := json.Unmarshal(parts[1].Message, &r); err != nil || r.Cols != 100 || r.Rows != 30 || r.Owner != "client:x" {
			t.Fatalf("cut %d: resize = %s", cut, parts[1].Message)
		}
	}
}

func TestDecoderHoldsPartialPrefix(t *testing.T) {
	d := NewDecoder(7)
	if p := d.Decode([]byte("ab\x1b_re")); len(p) != 1 || string(p[0].Output) != "ab" {
		t.Fatalf("parts = %+v", p)
	}
	if p := d.Decode([]byte("set")); len(p) != 1 || string(p[0].Output) != "\x1b_reset" {
		t.Fatalf("parts = %+v", p)
	}
}
