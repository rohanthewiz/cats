//go:build ghostty

// This file wraps go-libghostty's key/mouse/paste encoders. It only builds
// with `-tags ghostty` and requires libghostty-vt on PKG_CONFIG_PATH (the
// same prerequisite as internal/terminal's emulator; no Zig toolchain — the
// prebuilt static lib from herdr's vendor tree suffices).
//
// go-libghostty is pinned in go.mod and makes no API-stability promise yet,
// so all of its surface is confined to this file behind Encoder.

package inputenc

import (
	"runtime"

	libghostty "go.mitchellh.com/libghostty"

	"github.com/rohanthewiz/herdr-web/internal/browserproto"
	"github.com/rohanthewiz/herdr-web/internal/terminal"
)

// Encoder turns one pane's structured input events into the exact bytes its
// PTY should receive, honoring the pane's live mode state (SetModes, from the
// β pane_modes mirror). One Encoder per pane; not safe for concurrent use —
// the server serializes a pane's input anyway (ordering matters).
type Encoder struct {
	key        *libghostty.KeyEncoder
	mouse      *libghostty.MouseEncoder
	keyEvent   *libghostty.KeyEvent
	mouseEvent *libghostty.MouseEvent
	modes      terminal.InputModes
	pressed    uint8 // bitmask of held buttons (browserproto btn values 0-2)
}

// New creates an encoder for one pane. Close it when the pane goes away.
func New() (*Encoder, error) {
	e := &Encoder{}
	var err error
	if e.key, err = libghostty.NewKeyEncoder(); err != nil {
		return nil, err
	}
	if e.mouse, err = libghostty.NewMouseEncoder(); err != nil {
		e.Close()
		return nil, err
	}
	if e.keyEvent, err = libghostty.NewKeyEvent(); err != nil {
		e.Close()
		return nil, err
	}
	if e.mouseEvent, err = libghostty.NewMouseEvent(); err != nil {
		e.Close()
		return nil, err
	}
	// Terminal users expect the option/alt key to send ESC-prefixed input,
	// not composed characters (macOS). Becomes a config knob when WS2 ports
	// herdr's macos-option-as-alt setting.
	e.key.SetOptOptionAsAlt(libghostty.OptionAsAltTrue)
	// DEC mode 1036 (altSendsEscape) defaults ON in the terminal
	// (modes.zig:289) but OFF in the standalone encoder. β doesn't mirror
	// 1036 changes — matching herdr's Rust encoder, which always prefixed —
	// so pin the terminal default here.
	e.key.SetOptBool(libghostty.KeyEncoderOptAltEscPrefix, true)
	// Cell coordinates go straight through: 1px cells make surface-space
	// positions equal cell positions. SetGrid refines the clamp area.
	e.mouse.SetOptSize(libghostty.MouseEncoderSize{
		ScreenWidth: 65535, ScreenHeight: 65535, CellWidth: 1, CellHeight: 1,
	})
	return e, nil
}

// Close frees the underlying libghostty handles.
func (e *Encoder) Close() {
	if e.keyEvent != nil {
		e.keyEvent.Close()
	}
	if e.mouseEvent != nil {
		e.mouseEvent.Close()
	}
	if e.mouse != nil {
		e.mouse.Close()
	}
	if e.key != nil {
		e.key.Close()
	}
	*e = Encoder{}
}

// SetModes applies the pane's current input-affecting mode state. Call it on
// every β pane_modes event; encoding between calls uses the previous state.
func (e *Encoder) SetModes(m terminal.InputModes) {
	e.modes = m
	e.key.SetOptBool(libghostty.KeyEncoderOptCursorKeyApplication, m.ApplicationCursor)
	e.key.SetOptBool(libghostty.KeyEncoderOptModifyOtherKeysState2, m.ModifyOtherKeys)
	e.key.SetOptKittyFlags(libghostty.KittyKeyFlags(m.KittyKeyboardFlags))
	e.mouse.SetOptTrackingMode(trackingMode(m.MouseMode))
	e.mouse.SetOptFormat(mouseFormat(m.MouseEncoding))
}

// SetGrid sets the pane's cell grid, bounding mouse coordinates. Call on
// create/resize.
func (e *Encoder) SetGrid(cols, rows uint16) {
	e.mouse.SetOptSize(libghostty.MouseEncoderSize{
		ScreenWidth:  uint32(cols),
		ScreenHeight: uint32(rows),
		CellWidth:    1,
		CellHeight:   1,
	})
}

// Modes returns the last applied mode state.
func (e *Encoder) Modes() terminal.InputModes { return e.modes }

// Key encodes a structured key event. A nil result with nil error means the
// event produces no bytes under the current modes (e.g. a bare modifier, or
// a release the pane didn't ask to see).
func (e *Encoder) Key(k browserproto.Key) ([]byte, error) {
	ev := e.keyEvent
	switch k.Kind {
	case browserproto.KeyDown:
		ev.SetAction(libghostty.KeyActionPress)
	case browserproto.KeyRepeat:
		ev.SetAction(libghostty.KeyActionRepeat)
	case browserproto.KeyUp:
		ev.SetAction(libghostty.KeyActionRelease)
	default:
		return nil, nil
	}

	gk, err := libghostty.ParseKey(w3cKeyName(k.Code))
	if err != nil {
		gk = libghostty.KeyUnidentified
	}
	ev.SetKey(gk)
	ev.SetMods(keyMods(k.Mods))

	text := keyText(k.Key)
	unshifted := unshiftedCodepoint(k.Code, k.Key, k.Mods&browserproto.ModAlt != 0)
	ev.SetUTF8(text)
	ev.SetUnshiftedCodepoint(unshifted)
	// Shift that changed the produced character was consumed by translation
	// ("A", "!"); shift on a non-text key (Shift+Arrow) was not.
	var consumed libghostty.Mods
	if text != "" && k.Mods&browserproto.ModShift != 0 && text != string(unshifted) {
		consumed = libghostty.ModShift
	}
	ev.SetConsumedMods(consumed)

	out, err := e.key.Encode(ev)
	runtime.KeepAlive(text) // SetUTF8 does not copy; keep it alive through Encode
	ev.SetUTF8("")          // drop the C-side pointer into text before it can dangle
	return out, err
}

// Mouse encodes a pointer event in pane cell coordinates. Wheel events under
// alternate scroll (alt screen + mode 1007 + no mouse reporting) become
// cursor keys; wheel with no reporting and no alternate scroll returns nil —
// that scroll belongs to the viewport (`cmd scroll`), not the PTY.
func (e *Encoder) Mouse(m browserproto.Mouse) ([]byte, error) {
	if m.Kind == browserproto.MouseWheel {
		return e.wheel(m)
	}

	ev := e.mouseEvent
	ev.SetMods(keyMods(m.Mods))
	ev.SetPosition(libghostty.MousePosition{X: float32(m.X), Y: float32(m.Y)})

	switch m.Kind {
	case browserproto.MouseDown:
		btn, ok := mouseButton(m.Btn)
		if !ok {
			return nil, nil
		}
		e.pressed |= 1 << m.Btn
		e.mouse.SetOptAnyButtonPressed(true)
		ev.SetAction(libghostty.MouseActionPress)
		ev.SetButton(btn)
	case browserproto.MouseUp:
		btn, ok := mouseButton(m.Btn)
		if !ok {
			return nil, nil
		}
		ev.SetAction(libghostty.MouseActionRelease)
		ev.SetButton(btn)
		defer func() {
			e.pressed &^= 1 << m.Btn
			e.mouse.SetOptAnyButtonPressed(e.pressed != 0)
		}()
	case browserproto.MouseMove:
		ev.SetAction(libghostty.MouseActionMotion)
		if btn, ok := mouseButton(m.Btn); ok {
			ev.SetButton(btn)
			e.mouse.SetOptAnyButtonPressed(true)
		} else {
			ev.ClearButton()
			e.mouse.SetOptAnyButtonPressed(e.pressed != 0)
		}
	default:
		return nil, nil
	}
	return e.mouse.Encode(ev)
}

// wheel encodes scroll deltas as one wheel-button press per line (X11
// buttons 4/5 vertical, 6/7 horizontal — 64-67 on the wire).
func (e *Encoder) wheel(m browserproto.Mouse) ([]byte, error) {
	if e.modes.MouseMode == terminal.MouseNone {
		if AlternateScrollActive(e.modes) {
			return EncodeAlternateScroll(m.DY, e.modes.ApplicationCursor), nil
		}
		return nil, nil
	}

	ev := e.mouseEvent
	ev.SetMods(keyMods(m.Mods))
	ev.SetPosition(libghostty.MousePosition{X: float32(m.X), Y: float32(m.Y)})
	ev.SetAction(libghostty.MouseActionPress)

	var out []byte
	emit := func(btn libghostty.MouseButton, times int) error {
		ev.SetButton(btn)
		for range times {
			b, err := e.mouse.Encode(ev)
			if err != nil {
				return err
			}
			out = append(out, b...)
		}
		return nil
	}
	var err error
	switch {
	case m.DY < 0:
		err = emit(libghostty.MouseButtonFour, -m.DY) // wheel up
	case m.DY > 0:
		err = emit(libghostty.MouseButtonFive, m.DY) // wheel down
	}
	if err != nil {
		return nil, err
	}
	switch {
	case m.DX < 0:
		err = emit(libghostty.MouseButtonSix, -m.DX) // wheel left
	case m.DX > 0:
		err = emit(libghostty.MouseButtonSeven, m.DX) // wheel right
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Paste prepares pasted text for the PTY: bracketed-paste markers when the
// pane requested them, ghostty's paste sanitization either way (unsafe
// control bytes replaced, newlines become carriage returns when unbracketed).
func (e *Encoder) Paste(text string) ([]byte, error) {
	return libghostty.PasteEncode([]byte(text), e.modes.BracketedPaste)
}

func keyMods(mods uint8) libghostty.Mods {
	var out libghostty.Mods
	if mods&browserproto.ModShift != 0 {
		out |= libghostty.ModShift
	}
	if mods&browserproto.ModAlt != 0 {
		out |= libghostty.ModAlt
	}
	if mods&browserproto.ModCtrl != 0 {
		out |= libghostty.ModCtrl
	}
	if mods&browserproto.ModMeta != 0 {
		out |= libghostty.ModSuper
	}
	return out
}

func mouseButton(btn uint8) (libghostty.MouseButton, bool) {
	switch btn {
	case browserproto.BtnLeft:
		return libghostty.MouseButtonLeft, true
	case browserproto.BtnMiddle:
		return libghostty.MouseButtonMiddle, true
	case browserproto.BtnRight:
		return libghostty.MouseButtonRight, true
	}
	return libghostty.MouseButtonUnknown, false
}

func trackingMode(m terminal.MouseMode) libghostty.MouseTrackingMode {
	switch m {
	case terminal.MouseX10:
		return libghostty.MouseTrackingX10
	case terminal.MousePressRelease:
		return libghostty.MouseTrackingNormal
	case terminal.MouseButtonMotion:
		return libghostty.MouseTrackingButton
	case terminal.MouseAnyMotion:
		return libghostty.MouseTrackingAny
	default:
		return libghostty.MouseTrackingNone
	}
}

func mouseFormat(enc terminal.MouseEncoding) libghostty.MouseFormat {
	switch enc {
	case terminal.MouseEncodingUTF8:
		return libghostty.MouseFormatUTF8
	case terminal.MouseEncodingSGR:
		return libghostty.MouseFormatSGR
	default:
		return libghostty.MouseFormatX10
	}
}
