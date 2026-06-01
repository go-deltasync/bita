package bita

import (
	"errors"
	"io"
	"testing"
)

func TestVarintRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 16384, 1 << 31, 1<<64 - 1} {
		b := appendVarint(nil, v)
		p := newProtoBuf(b)
		got, err := p.readVarint()
		if err != nil {
			t.Fatalf("readVarint(%d): %v", v, err)
		}
		if got != v {
			t.Fatalf("varint roundtrip: got %d want %d", got, v)
		}
		if !p.eof() {
			t.Fatalf("varint %d: trailing bytes", v)
		}
	}
}

func TestReadVarintEOF(t *testing.T) {
	p := newProtoBuf(nil)
	if _, err := p.readVarint(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("empty readVarint err = %v", err)
	}
	// continuation bit set but no following byte
	p = newProtoBuf([]byte{0x80})
	if _, err := p.readVarint(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated readVarint err = %v", err)
	}
}

func TestReadVarintOverflow(t *testing.T) {
	// 10 bytes: nine continuation bytes then a 10th with value > 1.
	b := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}
	p := newProtoBuf(b)
	if _, err := p.readVarint(); !errors.Is(err, errVarintOverflow) {
		t.Fatalf("overflow err = %v", err)
	}
}

func TestReadTagEOF(t *testing.T) {
	p := newProtoBuf(nil)
	if _, _, err := p.readTag(); err == nil {
		t.Fatal("expected EOF from readTag")
	}
}

func TestReadBytes(t *testing.T) {
	p := newProtoBuf(append(appendVarint(nil, 3), 'a', 'b', 'c'))
	got, err := p.readBytes()
	if err != nil || string(got) != "abc" {
		t.Fatalf("readBytes = %q, %v", got, err)
	}
	// length exceeds remaining
	p = newProtoBuf(append(appendVarint(nil, 5), 'a', 'b'))
	if _, err := p.readBytes(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("readBytes overrun err = %v", err)
	}
	// length prefix itself truncated
	p = newProtoBuf([]byte{0x80})
	if _, err := p.readBytes(); err == nil {
		t.Fatal("expected error from truncated length")
	}
}

func TestAdvance(t *testing.T) {
	p := newProtoBuf([]byte{1, 2, 3, 4})
	if err := p.advance(2); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := p.advance(10); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("advance overrun err = %v", err)
	}
	if err := p.advance(-1); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("advance negative err = %v", err)
	}
}

func TestReadValue(t *testing.T) {
	// varint
	if v, _, err := newProtoBuf(appendVarint(nil, 300)).readValue(wireVarint); err != nil || v != 300 {
		t.Fatalf("readValue varint: %d, %v", v, err)
	}
	// 64-bit (skipped)
	if _, _, err := newProtoBuf([]byte{1, 2, 3, 4, 5, 6, 7, 8}).readValue(wireI64); err != nil {
		t.Fatalf("readValue i64: %v", err)
	}
	if _, _, err := newProtoBuf([]byte{1, 2, 3}).readValue(wireI64); err == nil {
		t.Fatal("readValue i64 short: expected err")
	}
	// len-delimited
	if _, raw, err := newProtoBuf(append(appendVarint(nil, 2), 'x', 'y')).readValue(wireLen); err != nil || string(raw) != "xy" {
		t.Fatalf("readValue len: %q, %v", raw, err)
	}
	// 32-bit (skipped)
	if _, _, err := newProtoBuf([]byte{1, 2, 3, 4}).readValue(wireI32); err != nil {
		t.Fatalf("readValue i32: %v", err)
	}
	if _, _, err := newProtoBuf([]byte{1, 2}).readValue(wireI32); err == nil {
		t.Fatal("readValue i32 short: expected err")
	}
	// unknown wire type
	if _, _, err := newProtoBuf(nil).readValue(3); err == nil {
		t.Fatal("readValue unknown wire type: expected err")
	}
}

func TestParsePackedUint32(t *testing.T) {
	var packed []byte
	for _, v := range []uint32{1, 200, 70000} {
		packed = appendVarint(packed, uint64(v))
	}
	got, err := parsePackedUint32(packed, nil)
	if err != nil {
		t.Fatalf("packed: %v", err)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 200 || got[2] != 70000 {
		t.Fatalf("packed = %v", got)
	}
	// truncated varint inside body
	if _, err := parsePackedUint32([]byte{0x80}, nil); err == nil {
		t.Fatal("expected err for truncated packed body")
	}
}
