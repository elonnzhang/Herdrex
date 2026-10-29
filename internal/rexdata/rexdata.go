// Package rexdata encodes the rex.data.terminal.v1 messages (see docs/PROTOCOL.md §3).
package rexdata

import (
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
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

	RejectUnknownTicket = 0x01
	RejectUnavailable   = 0x03
	RejectMalformed     = 0x05
	RejectFailed        = 0x06
)

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

func Reject(reason byte) []byte { return []byte{msgReject, reason} }

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
