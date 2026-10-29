package endpoint

import "fmt"

// Generation is the endpoint protocol generation this package speaks.
const Generation = 1

// Named control kinds and core codecs of generation 1 (src/protocol/endpoint.rs).
const (
	kindHello       = "endpoint.hello.v1"
	kindWelcome     = "endpoint.welcome.v1"
	kindSnapshot    = "shell.snapshot.v1"
	kindAgentView   = "endpoint.agent-view.v1"
	kindCompletions = "endpoint.agent-completions.v1"
	kindPing        = "endpoint.health.ping.v1"
	kindPong        = "endpoint.health.pong.v1"

	codecSnapshot = "shell.snapshot.v1"
	codecSurface  = "shell.surface.v1"
	codecInput    = "shell.input.semantic.v1"
	codecBlob     = "shell.blob.v1"
)

// Hello is what the client announces in the handshake. The zero value is an
// inactive 80×24 connection: it receives snapshots and notifications, but no
// surface; it cannot send pane input, does not claim tab size or focus, and
// never becomes the foreground client (pkg/herdr/HERDR.md §6.7).
type Hello struct {
	Cols, Rows                uint16 // surface size; 0 means 80 or 24
	CellWidthPx, CellHeightPx uint32 // 0 when unknown
	PixelMouse                bool   // mouse input can carry exact pixel positions
	EndpointKeybindings       bool   // the server's keymap, not the client's, owns shell bindings
	MouseCapture              bool   // capture host mouse input even when no pane asks for it
	SurfaceActive             bool   // receive the surface and accept input from the start
}

// helloJSON is EndpointClientHello. surface_active is always written,
// because Herdr defaults a missing one to true.
type helloJSON struct {
	Generation          uint32      `json:"generation"`
	CellWidthPx         uint32      `json:"cell_width_px"`
	CellHeightPx        uint32      `json:"cell_height_px"`
	SurfaceSize         surfaceSize `json:"surface_size"`
	PixelMouse          bool        `json:"pixel_mouse"`
	DirectGraphics      bool        `json:"direct_graphics"`
	EndpointKeybindings bool        `json:"endpoint_keybindings"`
	MouseCapture        bool        `json:"mouse_capture"`
	SurfaceActive       bool        `json:"surface_active"`
	SurfaceReuse        bool        `json:"surface_reuse"`
	SurfaceDelta        bool        `json:"surface_delta"`
	SurfaceScroll       bool        `json:"surface_scroll"`
	SnapshotCodecs      []string    `json:"snapshot_codecs"`
	SurfaceCodecs       []string    `json:"surface_codecs"`
	InputCodecs         []string    `json:"input_codecs"`
	BlobCodecs          []string    `json:"blob_codecs"`
}

type surfaceSize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (h Hello) json() helloJSON {
	size := surfaceSize{h.Cols, h.Rows}
	if size.Cols == 0 {
		size.Cols = 80
	}
	if size.Rows == 0 {
		size.Rows = 24
	}
	return helloJSON{
		Generation:          Generation,
		CellWidthPx:         h.CellWidthPx,
		CellHeightPx:        h.CellHeightPx,
		SurfaceSize:         size,
		PixelMouse:          h.PixelMouse,
		EndpointKeybindings: h.EndpointKeybindings,
		MouseCapture:        h.MouseCapture,
		SurfaceActive:       h.SurfaceActive,
		SnapshotCodecs:      []string{codecSnapshot},
		SurfaceCodecs:       []string{codecSurface},
		InputCodecs:         []string{codecInput},
		BlobCodecs:          []string{codecBlob},
	}
}

// Welcome is the server's handshake reply (EndpointServerWelcome).
type Welcome struct {
	Generation    uint32          `json:"generation"`
	ServerVersion string          `json:"server_version"`
	SnapshotCodec string          `json:"snapshot_codec"`
	SurfaceCodec  string          `json:"surface_codec"`
	InputCodec    string          `json:"input_codec"`
	BlobCodec     string          `json:"blob_codec"`
	Methods       []string        `json:"methods"`      // the API methods Call accepts
	Capabilities  []string        `json:"capabilities"` // such as "surface_interest" and "health_check"
	Error         *HandshakeError `json:"error"`        // set when the server rejected the hello
}

// check returns the server's rejection, or a HandshakeError when the server
// chose something other than generation 1 and its core codecs.
func (w *Welcome) check() error {
	if w.Error != nil {
		e := *w.Error
		e.ServerVersion = w.ServerVersion
		return &e
	}
	if w.Generation != Generation {
		return &HandshakeError{Code: "unsupported_generation", ServerVersion: w.ServerVersion,
			Message: fmt.Sprintf("server chose endpoint generation %d", w.Generation)}
	}
	if w.SnapshotCodec != codecSnapshot || w.SurfaceCodec != codecSurface ||
		w.InputCodec != codecInput || w.BlobCodec != codecBlob {
		return &HandshakeError{Code: "no_common_core", ServerVersion: w.ServerVersion,
			Message: fmt.Sprintf("server chose codecs %s, %s, %s, %s",
				w.SnapshotCodec, w.SurfaceCodec, w.InputCodec, w.BlobCodec)}
	}
	return nil
}

// HandshakeError is a hello the server rejected, with Herdr's code:
// invalid_hello, unsupported_generation, no_common_core or invalid_surface.
// Dial also returns one, with unsupported_generation or no_common_core, when
// the server accepts with a generation or codec other than the ones asked for.
type HandshakeError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	ServerVersion string `json:"-"`
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("herdr %s rejected the endpoint handshake: %s: %s", e.ServerVersion, e.Code, e.Message)
}

// NoEndpointError means the server does not speak the endpoint protocol: it
// answered the hello with a direct terminal protocol Welcome (Herdr 0.9.5
// does so for any first message it does not recognize), or it closed the
// connection without answering (servers before 0.9.0). Message is the
// server's reason, when it gave one.
type NoEndpointError struct{ Message string }

func (e *NoEndpointError) Error() string {
	return "herdr: server does not support the endpoint protocol: " + e.Message
}
