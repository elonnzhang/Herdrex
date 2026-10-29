// Package rex holds the types and names of Rex's control protocol
// rex.control.v1 that its clients and servers share (docs/rex-api.md).
// Package client is the client half, package server the server half, and
// package termstream the terminal data protocol rex.data.terminal.v1.
package rex

import "encoding/json"

const (
	ControlProtocol = "rex.control.v1"
	DataProtocol    = "rex.data.terminal.v1"
	APIRevision     = 7
	MaxMessageBytes = 1 << 20 // limits.max_message_bytes, counted uncompressed
)

// The terminal block creator and its shell flavor.
const (
	TerminalCreator = "com.superlogical.terminal"
	ShellFlavor     = "com.superlogical.terminal.shell"
)

// Error codes (docs/rex-api.md 2.3). Clients branch on the code and treat
// codes they do not know by status.
const (
	CodeBadRequest        = "bad_request"
	CodeNotFound          = "not_found"
	CodeMethodNotFound    = "method_not_found"
	CodeSessionNotFound   = "session_not_found"
	CodeConflict          = "conflict"
	CodeNotSupported      = "not_supported"
	CodeRefused           = "refused"
	CodeClientUnavailable = "client_unavailable"
	CodeTooLarge          = "too_large"
	CodeRevisionTooLow    = "revision_too_low"
	CodeTimeout           = "timeout"
	CodeInternal          = "internal"
)

// Error is the error of a failed request.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func NewError(code, message string, status int) *Error { return &Error{code, message, status} }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Close codes of data connections (docs/rex-api.md 5.3).
const (
	CloseNormal              = 1000 // the stream ended
	CloseProviderCanceled    = 1001
	CloseProviderUnavailable = 1008
	CloseProviderFailed      = 1011
	CloseReconnectRequired   = 1013 // reconnect with a new ticket
	CloseBlockClosed         = 4000 // do not reconnect
)

// Hello is the client's first message.
type Hello struct {
	Type           string         `json:"type"`
	APIRevision    int            `json:"api_revision"`
	APIMinRevision int            `json:"api_min_revision"`
	ReconnectToken string         `json:"reconnect_token,omitempty"`
	Client         map[string]any `json:"client,omitempty"` // kind, platform, name, version, label
}

// Welcome is the server's first message.
type Welcome struct {
	Type              string        `json:"type"`
	Protocol          string        `json:"protocol"`
	Limits            Limits        `json:"limits"`
	Client            WelcomeClient `json:"client"`
	APIRevision       int           `json:"api_revision"`
	APIServerRevision int           `json:"api_server_revision"`
}

type Limits struct {
	MaxMessageBytes int `json:"max_message_bytes"`
}

type WelcomeClient struct {
	ID             string `json:"id"`
	ReconnectToken string `json:"reconnect_token"`
	Resumed        bool   `json:"resumed"`
}

type Request struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response answers a request. Seq is the last event the server had queued
// for the connection when it started on the request: events up to Seq are
// already part of the result.
type Response struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Seq     uint64          `json:"seq"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Event is the payload of an event message, such as session_created or
// session_event.
type Event struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// SessionEvent is the payload of a session_event.
type SessionEvent struct {
	SessionID string `json:"session_id"`
	Event     Event  `json:"event"`
}

// BlockEvent is the payload of a block_event session event.
type BlockEvent struct {
	BlockID     string          `json:"block_id"`
	CreatorName string          `json:"creator_name,omitempty"`
	Name        string          `json:"name"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}
