// Package termstream encodes and decodes the terminal data protocol
// rex.data.terminal.v1 (docs/rex-api.md 5): the handshake messages, and the
// Rex messages a server puts between program output.
package termstream

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
)

// BlankSnapshot is a Ghostty snapshot of an empty 80x24 terminal, captured
// from a real Rex server. Its length and checksum fields make it unsafe to
// build by hand, so it is replayed as is and followed by a resize message.
//
//go:embed blank_80x24.snap
var BlankSnapshot []byte

const (
	msgHello   = 0x01
	msgWelcome = 0x81
	msgReject  = 0x82

	// MaxFrame is the most a server sends in one binary frame.
	MaxFrame = 64 << 10
)

// REJECT reasons.
const (
	RejectUnknownTicket = 0x01 // invalid, expired, used or someone else's ticket or token
	RejectDuplicate     = 0x02 // the client already streams this data source
	RejectUnavailable   = 0x03
	RejectLimit         = 0x04
	RejectMalformed     = 0x05
	RejectFailed        = 0x06
	RejectSubprotocol   = 0x07
)

// Hello is the client's first message, with the ticket from data.connect.
func Hello(ticket [16]byte) []byte { return append([]byte{msgHello}, ticket[:]...) }

// ParseHello returns the 16 ticket bytes of a HELLO. Bytes after the fixed
// part are ignored, as the protocol allows.
func ParseHello(b []byte) (t [16]byte, ok bool) {
	if len(b) < 17 || b[0] != msgHello {
		return t, false
	}
	copy(t[:], b[1:17])
	return t, true
}

func Welcome(token [16]byte, keepMillis uint32, nonce uint64) []byte {
	b := make([]byte, 32)
	b[0] = msgWelcome
	copy(b[1:17], token[:])
	binary.LittleEndian.PutUint32(b[20:24], keepMillis)
	binary.LittleEndian.PutUint64(b[24:32], nonce)
	return b
}

// ParseWelcome reads a WELCOME: the reconnect token, how long the server
// keeps the stream after a disconnect, and the nonce of its Rex messages.
func ParseWelcome(b []byte) (token [16]byte, keepMillis uint32, nonce uint64, ok bool) {
	if len(b) < 32 || b[0] != msgWelcome {
		return token, 0, 0, false
	}
	copy(token[:], b[1:17])
	return token, binary.LittleEndian.Uint32(b[20:24]), binary.LittleEndian.Uint64(b[24:32]), true
}

func Reject(reason byte) []byte { return []byte{msgReject, reason} }

// ParseReject returns the reason of a REJECT.
func ParseReject(b []byte) (reason byte, ok bool) {
	if len(b) < 2 || b[0] != msgReject {
		return 0, false
	}
	return b[1], true
}

// Resize is the APC message that tells the client the terminal size.
func Resize(nonce uint64, cols, rows, cellW, cellH int, owner string) []byte {
	m := map[string]any{"type": "resize", "cols": cols, "rows": rows,
		"cell_width_px": cellW, "cell_height_px": cellH, "owner": nil}
	if owner != "" {
		m["owner"] = owner
	}
	return apc(nonce, m)
}

// ChildExited is the APC message that ends a terminal's program output.
func ChildExited(nonce uint64, code int) []byte {
	return apc(nonce, map[string]any{"type": "child_exited", "exit_code": code})
}

// Theme is the APC message with the terminal's default colors; body holds
// the set_theme arguments.
func Theme(nonce uint64, body map[string]any) []byte {
	m := map[string]any{"type": "theme"}
	for k, v := range body {
		m[k] = v
	}
	return apc(nonce, m)
}

func apc(nonce uint64, m map[string]any) []byte {
	j, _ := json.Marshal(m)
	return []byte(fmt.Sprintf("\x1b_rex1;%d;%s\x1b\\", nonce, j))
}

// Part is a piece of a terminal stream: program output, or a Rex message
// when Type is set. The stream's first output is the terminal snapshot.
type Part struct {
	Output  []byte
	Type    string          // Rex message type, such as "resize"
	Message json.RawMessage // the whole message
}

// maxMessage bounds a Rex message; a longer one is taken as output.
const maxMessage = 1 << 20

// Decoder splits a terminal stream into output and the Rex messages that
// carry its nonce. Messages may be split across frames; APC sequences with
// another nonce are program output.
type Decoder struct {
	prefix []byte
	buf    []byte
}

func NewDecoder(nonce uint64) *Decoder {
	return &Decoder{prefix: []byte("\x1b_rex1;" + strconv.FormatUint(nonce, 10) + ";")}
}

// Decode takes the next bytes of the stream and returns the parts they
// complete. It holds back the start of a message until its end arrives.
func (d *Decoder) Decode(p []byte) []Part {
	d.buf = append(d.buf, p...)
	var out []Part
	output := func(b []byte) {
		if len(b) > 0 {
			out = append(out, Part{Output: bytes.Clone(b)})
		}
	}
	for {
		i := bytes.Index(d.buf, d.prefix)
		if i < 0 {
			keep := partialPrefix(d.buf, d.prefix)
			output(d.buf[:len(d.buf)-keep])
			d.buf = append(d.buf[:0], d.buf[len(d.buf)-keep:]...)
			return out
		}
		output(d.buf[:i])
		body := d.buf[i+len(d.prefix):]
		j := bytes.Index(body, []byte("\x1b\\"))
		if j < 0 {
			if len(body) > maxMessage {
				output(d.buf[i:])
				d.buf = d.buf[:0]
				return out
			}
			d.buf = append(d.buf[:0], d.buf[i:]...)
			return out
		}
		var m struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(body[:j], &m) == nil && m.Type != "" {
			out = append(out, Part{Type: m.Type, Message: bytes.Clone(body[:j])})
		} else {
			output(d.buf[i : i+len(d.prefix)+j+2])
		}
		d.buf = body[j+2:]
	}
}

// partialPrefix is the length of the longest end of b that starts prefix.
func partialPrefix(b, prefix []byte) int {
	for n := min(len(b), len(prefix)-1); n > 0; n-- {
		if bytes.HasPrefix(prefix, b[len(b)-n:]) {
			return n
		}
	}
	return 0
}
