// Package wire implements just enough of herdr's client/server wire protocol
// (src/protocol/wire.rs) for a non-Rust client to attach to a running herdr
// server: length-prefixed frames carrying a bincode v2 "standard" payload.
//
// bincode "standard" config means: little-endian, variable-length integer
// encoding, enum discriminants written as a varint u32, Option<T> as a 1-byte
// 0/1 tag, and Vec/String length-prefixed by a varint usize.
package wire

import (
	"encoding/binary"
	"errors"
	"math"
)

// ErrShortBuffer is returned when a decode runs past the end of the payload.
var ErrShortBuffer = errors.New("wire: unexpected end of payload")

// Encoder builds a bincode "standard" payload.
type Encoder struct{ buf []byte }

// NewEncoder returns an empty Encoder.
func NewEncoder() *Encoder { return &Encoder{} }

// Payload returns the accumulated bytes.
func (e *Encoder) Payload() []byte { return e.buf }

// Byte writes a single raw byte.
func (e *Encoder) Byte(b byte) { e.buf = append(e.buf, b) }

// Bool writes a bool as a single 0/1 byte.
func (e *Encoder) Bool(v bool) {
	if v {
		e.Byte(1)
	} else {
		e.Byte(0)
	}
}

// Uvarint writes an unsigned integer using bincode's variable scheme:
// values < 251 are a single byte; otherwise a marker byte (251=u16, 252=u32,
// 253=u64) followed by that many little-endian bytes.
func (e *Encoder) Uvarint(v uint64) {
	switch {
	case v < 251:
		e.buf = append(e.buf, byte(v))
	case v <= math.MaxUint16:
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(v))
		e.buf = append(e.buf, 251, b[0], b[1])
	case v <= math.MaxUint32:
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		e.buf = append(e.buf, 252)
		e.buf = append(e.buf, b[:]...)
	default:
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], v)
		e.buf = append(e.buf, 253)
		e.buf = append(e.buf, b[:]...)
	}
}

// U16, U32, U64 write unsigned integers as varints.
func (e *Encoder) U16(v uint16) { e.Uvarint(uint64(v)) }
func (e *Encoder) U32(v uint32) { e.Uvarint(uint64(v)) }
func (e *Encoder) U64(v uint64) { e.Uvarint(v) }

// Variant writes an enum discriminant (a varint u32).
func (e *Encoder) Variant(idx uint32) { e.Uvarint(uint64(idx)) }

// Str writes a UTF-8 string: varint length then raw bytes.
func (e *Encoder) Str(s string) {
	e.Uvarint(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

// ByteSlice writes a Vec<u8>: varint length then raw bytes.
func (e *Encoder) ByteSlice(b []byte) {
	e.Uvarint(uint64(len(b)))
	e.buf = append(e.buf, b...)
}

// Decoder reads a bincode "standard" payload.
type Decoder struct {
	data []byte
	pos  int
}

// NewDecoder wraps a payload for decoding.
func NewDecoder(data []byte) *Decoder { return &Decoder{data: data} }

// Remaining reports how many bytes are left unread.
func (d *Decoder) Remaining() int { return len(d.data) - d.pos }

// Byte reads one raw byte.
func (d *Decoder) Byte() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, ErrShortBuffer
	}
	b := d.data[d.pos]
	d.pos++
	return b, nil
}

// Bool reads a single 0/1 byte.
func (d *Decoder) Bool() (bool, error) {
	b, err := d.Byte()
	return b != 0, err
}

// Uvarint reads a bincode variable-length unsigned integer.
func (d *Decoder) Uvarint() (uint64, error) {
	b, err := d.Byte()
	if err != nil {
		return 0, err
	}
	switch {
	case b < 251:
		return uint64(b), nil
	case b == 251:
		return d.fixed(2)
	case b == 252:
		return d.fixed(4)
	case b == 253:
		return d.fixed(8)
	default:
		return 0, errors.New("wire: u128 varint unsupported")
	}
}

func (d *Decoder) fixed(n int) (uint64, error) {
	if d.Remaining() < n {
		return 0, ErrShortBuffer
	}
	var v uint64
	for i := 0; i < n; i++ {
		v |= uint64(d.data[d.pos+i]) << (8 * uint(i))
	}
	d.pos += n
	return v, nil
}

// U16, U32, U64 read unsigned integers from varints.
func (d *Decoder) U16() (uint16, error) { v, err := d.Uvarint(); return uint16(v), err }
func (d *Decoder) U32() (uint32, error) { v, err := d.Uvarint(); return uint32(v), err }
func (d *Decoder) U64() (uint64, error) { return d.Uvarint() }

// Variant reads an enum discriminant (varint u32).
func (d *Decoder) Variant() (uint32, error) { v, err := d.Uvarint(); return uint32(v), err }

// Str reads a length-prefixed UTF-8 string.
func (d *Decoder) Str() (string, error) {
	n, err := d.Uvarint()
	if err != nil {
		return "", err
	}
	if uint64(d.Remaining()) < n {
		return "", ErrShortBuffer
	}
	s := string(d.data[d.pos : d.pos+int(n)])
	d.pos += int(n)
	return s, nil
}

// ByteSlice reads a length-prefixed Vec<u8>.
func (d *Decoder) ByteSlice() ([]byte, error) {
	n, err := d.Uvarint()
	if err != nil {
		return nil, err
	}
	if uint64(d.Remaining()) < n {
		return nil, ErrShortBuffer
	}
	b := d.data[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return b, nil
}

// OptionTag reads an Option<T> discriminant: false=None, true=Some.
func (d *Decoder) OptionTag() (bool, error) {
	b, err := d.Byte()
	return b != 0, err
}
