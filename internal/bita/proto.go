package bita

import (
	"errors"
	"fmt"
	"io"
)

// Minimal protobuf (proto3) codec, hand-rolled so the on-the-wire bytes stay
// fully under our control and the package keeps 100% test coverage without a
// generated .pb.go. Only the wire types used by bita's ChunkDictionary are
// implemented; unknown fields are skipped so we stay forward-compatible with
// future bita versions.

// Protobuf wire types.
const (
	wireVarint = 0
	wireI64    = 1
	wireLen    = 2
	wireI32    = 5
)

var errVarintOverflow = errors.New("bita: varint overflows uint64")

// appendVarint appends v as a base-128 varint.
func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// appendTag appends a field tag (field number + wire type).
func appendTag(dst []byte, field int, wt byte) []byte {
	return appendVarint(dst, uint64(field)<<3|uint64(wt))
}

// appendVarintField appends a varint-typed field, omitting proto3 zero defaults.
func appendVarintField(dst []byte, field int, v uint64) []byte {
	if v == 0 {
		return dst
	}
	dst = appendTag(dst, field, wireVarint)
	return appendVarint(dst, v)
}

// appendBytesField appends a length-delimited field, omitting empty values.
func appendBytesField(dst []byte, field int, v []byte) []byte {
	if len(v) == 0 {
		return dst
	}
	dst = appendTag(dst, field, wireLen)
	dst = appendVarint(dst, uint64(len(v)))
	return append(dst, v...)
}

// appendMessageField appends a length-delimited embedded message. Unlike scalar
// fields an (always-present) message is emitted even when empty, matching
// prost's behaviour for a Some(default) message field.
func appendMessageField(dst []byte, field int, msg []byte) []byte {
	dst = appendTag(dst, field, wireLen)
	dst = appendVarint(dst, uint64(len(msg)))
	return append(dst, msg...)
}

// appendPackedUint32 appends a packed repeated uint32 field, omitting empty.
func appendPackedUint32(dst []byte, field int, vals []uint32) []byte {
	if len(vals) == 0 {
		return dst
	}
	var packed []byte
	for _, v := range vals {
		packed = appendVarint(packed, uint64(v))
	}
	dst = appendTag(dst, field, wireLen)
	dst = appendVarint(dst, uint64(len(packed)))
	return append(dst, packed...)
}

// protoBuf is a cursor over a protobuf-encoded byte slice.
type protoBuf struct {
	b   []byte
	off int
}

func newProtoBuf(b []byte) *protoBuf { return &protoBuf{b: b} }

func (p *protoBuf) eof() bool { return p.off >= len(p.b) }

// readVarint reads a base-128 varint with an overflow guard.
func (p *protoBuf) readVarint() (uint64, error) {
	var x uint64
	var s uint
	for i := 0; ; i++ {
		if p.off >= len(p.b) {
			return 0, io.ErrUnexpectedEOF
		}
		b := p.b[p.off]
		p.off++
		if i == 9 && b > 1 {
			return 0, errVarintOverflow
		}
		if b < 0x80 {
			return x | uint64(b)<<s, nil
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
}

// readTag reads a field number and wire type.
func (p *protoBuf) readTag() (int, byte, error) {
	t, err := p.readVarint()
	if err != nil {
		return 0, 0, err
	}
	return int(t >> 3), byte(t & 7), nil
}

// advance skips n bytes.
func (p *protoBuf) advance(n int) error {
	if n < 0 || n > len(p.b)-p.off {
		return io.ErrUnexpectedEOF
	}
	p.off += n
	return nil
}

// readBytes reads a length-delimited byte slice (aliasing the backing array).
func (p *protoBuf) readBytes() ([]byte, error) {
	n, err := p.readVarint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(p.b)-p.off) {
		return nil, io.ErrUnexpectedEOF
	}
	out := p.b[p.off : p.off+int(n)]
	p.off += int(n)
	return out, nil
}

// readValue reads (and returns) the value of a field given its wire type. For
// varint fields the integer is returned; for length-delimited fields the byte
// slice is returned; fixed 32/64-bit fields are skipped (we never use them, but
// must tolerate them in unknown fields). This collapses per-field error
// handling at the call site to a single check.
func (p *protoBuf) readValue(wt byte) (uint64, []byte, error) {
	switch wt {
	case wireVarint:
		v, err := p.readVarint()
		return v, nil, err
	case wireI64:
		return 0, nil, p.advance(8)
	case wireLen:
		b, err := p.readBytes()
		return 0, b, err
	case wireI32:
		return 0, nil, p.advance(4)
	default:
		return 0, nil, fmt.Errorf("bita: unknown protobuf wire type %d", wt)
	}
}

// parsePackedUint32 parses a packed repeated uint32 body, appending to dst.
func parsePackedUint32(body []byte, dst []uint32) ([]uint32, error) {
	sub := newProtoBuf(body)
	for !sub.eof() {
		v, err := sub.readVarint()
		if err != nil {
			return nil, err
		}
		dst = append(dst, uint32(v))
	}
	return dst, nil
}
