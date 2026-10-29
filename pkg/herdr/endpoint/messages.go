package endpoint

import (
	"encoding/json"
	"fmt"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
)

// Message is one message from the server: *Snapshot, *Surface,
// *SurfacePatch, *Notification, *MouseCaptureState, *KeyboardReportAll,
// *Bell, *Clipboard, *WindowTitle, *Notify, *ShellError, *AgentView,
// *AgentCompletions, *Graphics, *Control, *Shutdown or *Other.
type Message interface{ message() }

type (
	// Shutdown says the server is closing the connection, for example with
	// "live update in progress; reconnect after handoff completes".
	Shutdown struct{ Reason string }
	// Notify is a sound or toast for the foreground client to present.
	Notify struct {
		Kind    NotifyKind
		Message string
		Body    string
	}
	// Clipboard is an OSC 52 copy from a pane; Data is base64. Only the
	// foreground client receives it.
	Clipboard struct{ Data string }
	// WindowTitle is the foreground client's window title; empty restores
	// the default.
	WindowTitle struct{ Title string }
	// MouseCaptureState says whether the client should capture host mouse
	// input. Inactive connections always get false.
	MouseCaptureState struct{ Enabled, SGRPixels bool }
	// Bell reports BEL characters from one PTY read, for the foreground
	// client.
	Bell struct{ Count uint16 }
	// ShellError is an endpoint error the client must show.
	ShellError struct{ Message string }
	// KeyboardReportAll says whether the focused pane or popup needs every
	// key reported, releases included.
	KeyboardReportAll struct{ Enabled bool }
	// Graphics is kitty graphics bytes for the host terminal. Herdr 0.9.5
	// does not send it on endpoint connections.
	Graphics struct{ Bytes []byte }
	// Control is a named control this package does not decode, such as
	// endpoint.presentation.ready.v1.
	Control struct{ Kind, Data string }
	// Other is a message this package does not decode, by its variant
	// index (ServerMessage).
	Other struct{ Tag uint32 }
)

// NotifyKind is how a Notify asks to be presented.
type NotifyKind uint8

const (
	NotifySound NotifyKind = iota
	NotifyToast
	NotifySystemToast // through the host's notification service
)

// Notification is a semantic notification, sent to every endpoint
// connection, active or not (SemanticNotification). The client decides
// whether and how to present it.
type Notification struct {
	Kind        NotificationKind
	Title       string
	Body        string
	Sound       Sound
	Agent       string
	WorkspaceID string
	TabID       string
	PaneID      string
	Position    ToastPosition
}

type NotificationKind uint8

const (
	NeedsAttention NotificationKind = iota
	Finished
	UpdateInstalled
	Custom
)

// Sound is the sound a Notification asks for; SoundNone when it has none.
type Sound uint8

const (
	SoundNone Sound = iota
	SoundDone
	SoundRequest
)

// ToastPosition is where a Notification's toast goes; ToastDefault when the
// server does not say.
type ToastPosition uint8

const (
	ToastDefault ToastPosition = iota
	ToastTopLeft
	ToastTopRight
	ToastBottomLeft
	ToastBottomRight
)

// responseChunk is one piece of a Call's result
// (ClientShellEndpointResponseChunk); the read loop consumes it.
type responseChunk struct {
	BootID, RequestID string
	Final             bool
	Data              []byte
}

func (*Snapshot) message()          {}
func (*Surface) message()           {}
func (*SurfacePatch) message()      {}
func (*Notification) message()      {}
func (*MouseCaptureState) message() {}
func (*KeyboardReportAll) message() {}
func (*Bell) message()              {}
func (*Clipboard) message()         {}
func (*WindowTitle) message()       {}
func (*Notify) message()            {}
func (*ShellError) message()        {}
func (*AgentView) message()         {}
func (*AgentCompletions) message()  {}
func (*Graphics) message()          {}
func (*Control) message()           {}
func (*Shutdown) message()          {}
func (*Other) message()             {}
func (*responseChunk) message()     {}

// ServerMessage variants (frozen within endpoint generation 1).
const (
	sWelcome       = 0
	sGraphics      = 2
	sShutdown      = 3
	sNotify        = 4
	sClipboard     = 5
	sTitle         = 6
	sMouseCapture  = 8
	sBell          = 9
	sSurface       = 13
	sNotification  = 14
	sShellError    = 15
	sReportAll     = 17
	sResponseChunk = 18
	sPatch         = 19
	sControl       = 20
)

// decodeMessage decodes one ServerMessage payload. The payload must be used
// up exactly, except by variants it returns as *Other.
func decodeMessage(p []byte) (Message, error) {
	d := wire.NewDec(p)
	tag := d.U32()
	var m Message
	switch tag {
	case sGraphics:
		m = &Graphics{Bytes: getBytes(d)}
	case sShutdown:
		m = &Shutdown{Reason: getOptStr(d)}
	case sNotify:
		m = &Notify{Kind: NotifyKind(variant(d, 3)), Message: d.Str(), Body: getOptStr(d)}
	case sClipboard:
		m = &Clipboard{Data: d.Str()}
	case sTitle:
		m = &WindowTitle{Title: getOptStr(d)}
	case sMouseCapture:
		m = &MouseCaptureState{Enabled: d.Bool(), SGRPixels: d.Bool()}
	case sBell:
		m = &Bell{Count: d.U16()}
	case sSurface:
		m = getSurface(d)
	case sNotification:
		m = getNotification(d)
	case sShellError:
		m = &ShellError{Message: d.Str()}
	case sReportAll:
		m = &KeyboardReportAll{Enabled: d.Bool()}
	case sResponseChunk:
		m = &responseChunk{BootID: d.Str(), RequestID: d.Str(), Final: d.Bool(), Data: getBytes(d)}
	case sPatch:
		m = getPatch(d)
	case sControl:
		m = &Control{Kind: d.Str(), Data: d.Str()}
	default:
		if d.Err() != nil {
			return nil, wire.ErrMalformed
		}
		return &Other{Tag: tag}, nil
	}
	if d.Done() != nil {
		return nil, fmt.Errorf("%w %d", wire.ErrMalformed, tag)
	}
	if c, ok := m.(*Control); ok {
		return decodeControl(c)
	}
	return m, nil
}

// decodeControl decodes the JSON of the named controls this package knows.
func decodeControl(c *Control) (Message, error) {
	var m Message
	switch c.Kind {
	case kindSnapshot:
		m = new(Snapshot)
	case kindAgentView:
		m = new(AgentView)
	case kindCompletions:
		m = new(AgentCompletions)
	default:
		return c, nil
	}
	if err := json.Unmarshal([]byte(c.Data), m); err != nil {
		return nil, fmt.Errorf("herdr: invalid %s: %w", c.Kind, err)
	}
	return m, nil
}

// encodeMessage is decodeMessage's inverse, for tests and fake servers.
func encodeMessage(m Message) wire.Enc {
	var e wire.Enc
	switch m := m.(type) {
	case *Graphics:
		e.U(sGraphics).Bytes(m.Bytes)
	case *Shutdown:
		putOptStr(e.U(sShutdown), m.Reason)
	case *Notify:
		putOptStr(e.U(sNotify).U(uint64(m.Kind)).Str(m.Message), m.Body)
	case *Clipboard:
		e.U(sClipboard).Str(m.Data)
	case *WindowTitle:
		putOptStr(e.U(sTitle), m.Title)
	case *MouseCaptureState:
		e.U(sMouseCapture).Bool(m.Enabled).Bool(m.SGRPixels)
	case *Bell:
		e.U(sBell).U(uint64(m.Count))
	case *Surface:
		putSurface(e.U(sSurface), m)
	case *Notification:
		putNotification(e.U(sNotification), m)
	case *ShellError:
		e.U(sShellError).Str(m.Message)
	case *KeyboardReportAll:
		e.U(sReportAll).Bool(m.Enabled)
	case *responseChunk:
		e.U(sResponseChunk).Str(m.BootID).Str(m.RequestID).Bool(m.Final).Bytes(m.Data)
	case *SurfacePatch:
		putPatch(e.U(sPatch), m)
	case *Snapshot:
		return encodeJSONControl(kindSnapshot, m)
	case *AgentView:
		return encodeJSONControl(kindAgentView, m)
	case *AgentCompletions:
		return encodeJSONControl(kindCompletions, m)
	case *Control:
		e.U(sControl).Str(m.Kind).Str(m.Data)
	case *Other:
		e.U(uint64(m.Tag))
	}
	return e
}

func encodeJSONControl(kind string, v any) wire.Enc {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err) // the JSON types always marshal
	}
	var e wire.Enc
	e.U(sControl).Str(kind).Str(string(data))
	return e
}

func getNotification(d *wire.Dec) *Notification {
	n := &Notification{Kind: NotificationKind(variant(d, 4)), Title: d.Str(), Body: getOptStr(d)}
	if d.Opt() {
		n.Sound = Sound(variant(d, 2) + 1)
	}
	n.Agent, n.WorkspaceID, n.TabID, n.PaneID = getOptStr(d), getOptStr(d), getOptStr(d), getOptStr(d)
	if d.Opt() {
		n.Position = ToastPosition(variant(d, 4) + 1)
	}
	return n
}

func putNotification(e *wire.Enc, n *Notification) {
	e.U(uint64(n.Kind)).Str(n.Title)
	putOptStr(e, n.Body)
	e.Opt(n.Sound != SoundNone)
	if n.Sound != SoundNone {
		e.U(uint64(n.Sound - 1))
	}
	for _, s := range []string{n.Agent, n.WorkspaceID, n.TabID, n.PaneID} {
		putOptStr(e, s)
	}
	e.Opt(n.Position != ToastDefault)
	if n.Position != ToastDefault {
		e.U(uint64(n.Position - 1))
	}
}

// variant reads an enum variant index, marking d bad unless it is below n:
// Herdr's enums are closed within generation 1.
func variant(d *wire.Dec, n uint32) uint32 {
	v := d.U32()
	if v >= n {
		d.Fail()
		return 0
	}
	return v
}

// getSlice reads a sequence. It grows the slice as elements decode, so a
// forged length cannot allocate more than the payload's worth. An empty
// sequence is nil.
func getSlice[T any](d *wire.Dec, get func(*wire.Dec) T) []T {
	n := d.Len()
	var s []T
	if n > 0 {
		s = make([]T, 0, min(n, 4096))
	}
	for range n {
		if d.Err() != nil {
			return nil
		}
		s = append(s, get(d))
	}
	return s
}

func putSlice[T any](e *wire.Enc, s []T, put func(*wire.Enc, T)) {
	e.U(uint64(len(s)))
	for _, v := range s {
		put(e, v)
	}
}

func putStr(e *wire.Enc, s string) { e.Str(s) }

// getBytes reads a byte vector; empty is nil. The result aliases the payload.
func getBytes(d *wire.Dec) []byte {
	if b := d.Bytes(); len(b) > 0 {
		return b
	}
	return nil
}

// getOptStr reads an Option<String>; None is "".
func getOptStr(d *wire.Dec) string {
	if d.Opt() {
		return d.Str()
	}
	return ""
}

// putOptStr writes "" as None.
func putOptStr(e *wire.Enc, s string) {
	e.Opt(s != "")
	if s != "" {
		e.Str(s)
	}
}

func getOptU32(d *wire.Dec) *uint32 {
	if !d.Opt() {
		return nil
	}
	v := d.U32()
	return &v
}

func putOptU(e *wire.Enc, v *uint32) {
	e.Opt(v != nil)
	if v != nil {
		e.U(uint64(*v))
	}
}
