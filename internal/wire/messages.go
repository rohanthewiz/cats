package wire

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ProtocolVersion is the herdr client/server wire protocol this client speaks.
// The installed herdr 0.7.0 server reports protocol 14.
const ProtocolVersion = 14

// MaxFrameSize mirrors wire.rs MAX_FRAME_SIZE (2 MiB) for normal traffic.
const MaxFrameSize = 2 * 1024 * 1024

// RenderEncoding variant indices (wire.rs enum RenderEncoding order).
const (
	EncSemanticFrame uint32 = 0
	EncTerminalAnsi  uint32 = 1
)

// ClientLaunchMode variant indices.
const (
	LaunchApp            uint32 = 0
	LaunchTerminalAttach uint32 = 1
)

// ClientMessage variant indices (wire.rs enum ClientMessage order).
const (
	cmHello          uint32 = 0
	cmInput          uint32 = 1
	cmClipboardImage uint32 = 2
	cmResize         uint32 = 3
	cmDetach         uint32 = 4
	cmAttachTerminal uint32 = 5
	cmAttachScroll   uint32 = 6
	cmInputEvents    uint32 = 7
)

// ServerMessage variant indices (wire.rs enum ServerMessage order, proto 14).
// WindowTitle was inserted at index 7 in proto 14, shifting ReloadSoundConfig
// and MouseCapture down by one versus proto 13.
const (
	SMWelcome        uint32 = 0
	SMFrame          uint32 = 1
	SMTerminal       uint32 = 2
	SMGraphics       uint32 = 3
	SMServerShutdown uint32 = 4
	SMNotify         uint32 = 5
	SMClipboard      uint32 = 6
	SMWindowTitle    uint32 = 7
	SMReloadSound    uint32 = 8
	SMMouseCapture   uint32 = 9
)

// ClientInputEvent variant indices (wire.rs enum ClientInputEvent order).
const (
	cieKey         uint32 = 0
	cieMouse       uint32 = 1
	ciePaste       uint32 = 2
	cieFocusGained uint32 = 3
	cieFocusLost   uint32 = 4
)

// EncodeHello builds a ClientMessage::Hello frame payload. keybindings is fixed
// to ClientKeybindings::Server (variant 0, no fields) and launch_mode to App.
func EncodeHello(cols, rows uint16, cellW, cellH uint32) []byte {
	e := NewEncoder()
	e.Variant(cmHello)
	e.U32(ProtocolVersion)
	e.U16(cols)
	e.U16(rows)
	e.U32(cellW)
	e.U32(cellH)
	e.Variant(EncSemanticFrame) // requested_encoding
	e.Variant(0)                // keybindings: ClientKeybindings::Server
	e.Variant(LaunchApp)        // launch_mode
	return e.Payload()
}

// EncodeInput builds a ClientMessage::Input frame payload from raw input bytes.
func EncodeInput(data []byte) []byte {
	e := NewEncoder()
	e.Variant(cmInput)
	e.ByteSlice(data)
	return e.Payload()
}

// EncodePaste builds a ClientMessage::InputEvents payload carrying a single
// ClientInputEvent::Paste. Routing paste as a structured event lets the server
// apply bracketed-paste framing only when the focused app has it enabled.
func EncodePaste(text string) []byte {
	e := NewEncoder()
	e.Variant(cmInputEvents)
	e.Uvarint(1) // Vec<ClientInputEvent> length
	e.Variant(ciePaste)
	e.Str(text)
	return e.Payload()
}

// EncodeResize builds a ClientMessage::Resize frame payload.
func EncodeResize(cols, rows uint16, cellW, cellH uint32) []byte {
	e := NewEncoder()
	e.Variant(cmResize)
	e.U16(cols)
	e.U16(rows)
	e.U32(cellW)
	e.U32(cellH)
	return e.Payload()
}

// EncodeDetach builds a ClientMessage::Detach frame payload.
func EncodeDetach() []byte {
	e := NewEncoder()
	e.Variant(cmDetach)
	return e.Payload()
}

// Cell is one rendered grid cell (wire.rs CellData).
type Cell struct {
	Symbol    string
	FG        uint32
	BG        uint32
	Modifier  uint16
	Skip      bool
	Hyperlink *uint32
}

// Cursor is the frame cursor state (wire.rs CursorState).
type Cursor struct {
	X       uint16
	Y       uint16
	Visible bool
	Shape   uint8
}

// Frame is a rendered semantic frame (wire.rs FrameData).
type Frame struct {
	Cells      []Cell
	Width      uint16
	Height     uint16
	Cursor     *Cursor
	Hyperlinks []string
	Graphics   []byte
}

// Welcome is the handshake response (wire.rs ServerMessage::Welcome).
type Welcome struct {
	Version  uint32
	Encoding uint32
	Error    *string
}

// Notify is a server notification (wire.rs ServerMessage::Notify).
type Notify struct {
	Kind    uint32 // 0=Sound, 1=Toast, 2=SystemToast
	Message string
	Body    *string
}

// ServerMessage is a decoded server→client message. Only the variants this
// client cares about are populated; others set only Kind.
type ServerMessage struct {
	Kind        uint32
	Welcome     *Welcome
	Frame       *Frame
	Shutdown    *string // reason for ServerShutdown, if any
	Clipboard   *string // OSC 52 clipboard payload (base64)
	WindowTitle *string // nil when the message restores the default title
	Mouse       *bool   // MouseCapture.enabled
	Notify      *Notify
}

// DecodeServerMessage decodes one ServerMessage payload (after framing).
func DecodeServerMessage(payload []byte) (*ServerMessage, error) {
	d := NewDecoder(payload)
	kind, err := d.Variant()
	if err != nil {
		return nil, err
	}
	msg := &ServerMessage{Kind: kind}
	switch kind {
	case SMWelcome:
		w, err := decodeWelcome(d)
		if err != nil {
			return nil, err
		}
		msg.Welcome = w
	case SMFrame:
		f, err := decodeFrame(d)
		if err != nil {
			return nil, err
		}
		msg.Frame = f
	case SMServerShutdown:
		s, err := decodeOptString(d)
		if err != nil {
			return nil, err
		}
		msg.Shutdown = s
	case SMClipboard:
		s, err := d.Str()
		if err != nil {
			return nil, err
		}
		msg.Clipboard = &s
	case SMWindowTitle:
		s, err := decodeOptString(d)
		if err != nil {
			return nil, err
		}
		msg.WindowTitle = s
	case SMMouseCapture:
		b, err := d.Bool()
		if err != nil {
			return nil, err
		}
		msg.Mouse = &b
	case SMNotify:
		n, err := decodeNotify(d)
		if err != nil {
			return nil, err
		}
		msg.Notify = n
	default:
		// Terminal/Graphics/ReloadSoundConfig: not decoded; ignore by Kind.
	}
	return msg, nil
}

func decodeOptString(d *Decoder) (*string, error) {
	has, err := d.OptionTag()
	if err != nil || !has {
		return nil, err
	}
	s, err := d.Str()
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func decodeNotify(d *Decoder) (*Notify, error) {
	kind, err := d.Variant()
	if err != nil {
		return nil, err
	}
	message, err := d.Str()
	if err != nil {
		return nil, err
	}
	body, err := decodeOptString(d)
	if err != nil {
		return nil, err
	}
	return &Notify{Kind: kind, Message: message, Body: body}, nil
}

func decodeWelcome(d *Decoder) (*Welcome, error) {
	var w Welcome
	var err error
	if w.Version, err = d.U32(); err != nil {
		return nil, err
	}
	if w.Encoding, err = d.Variant(); err != nil {
		return nil, err
	}
	has, err := d.OptionTag()
	if err != nil {
		return nil, err
	}
	if has {
		s, err := d.Str()
		if err != nil {
			return nil, err
		}
		w.Error = &s
	}
	return &w, nil
}

func decodeFrame(d *Decoder) (*Frame, error) {
	var f Frame
	n, err := d.Uvarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(d.Remaining()) {
		return nil, fmt.Errorf("wire: cells len %d exceeds remaining %d", n, d.Remaining())
	}
	f.Cells = make([]Cell, n)
	for i := range f.Cells {
		c := &f.Cells[i]
		if c.Symbol, err = d.Str(); err != nil {
			return nil, err
		}
		if c.FG, err = d.U32(); err != nil {
			return nil, err
		}
		if c.BG, err = d.U32(); err != nil {
			return nil, err
		}
		if c.Modifier, err = d.U16(); err != nil {
			return nil, err
		}
		if c.Skip, err = d.Bool(); err != nil {
			return nil, err
		}
		has, err := d.OptionTag()
		if err != nil {
			return nil, err
		}
		if has {
			v, err := d.U32()
			if err != nil {
				return nil, err
			}
			c.Hyperlink = &v
		}
	}
	if f.Width, err = d.U16(); err != nil {
		return nil, err
	}
	if f.Height, err = d.U16(); err != nil {
		return nil, err
	}
	hasCur, err := d.OptionTag()
	if err != nil {
		return nil, err
	}
	if hasCur {
		var cur Cursor
		if cur.X, err = d.U16(); err != nil {
			return nil, err
		}
		if cur.Y, err = d.U16(); err != nil {
			return nil, err
		}
		if cur.Visible, err = d.Bool(); err != nil {
			return nil, err
		}
		shape, err := d.Byte()
		if err != nil {
			return nil, err
		}
		cur.Shape = shape
		f.Cursor = &cur
	}
	hn, err := d.Uvarint()
	if err != nil {
		return nil, err
	}
	f.Hyperlinks = make([]string, 0, hn)
	for i := uint64(0); i < hn; i++ {
		s, err := d.Str()
		if err != nil {
			return nil, err
		}
		f.Hyperlinks = append(f.Hyperlinks, s)
	}
	if f.Graphics, err = d.ByteSlice(); err != nil {
		return nil, err
	}
	return &f, nil
}

// WriteFrame writes a length-prefixed frame: [u32-LE length][payload].
func WriteFrame(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one length-prefixed frame payload.
func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("wire: frame length %d exceeds max %d", n, MaxFrameSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
