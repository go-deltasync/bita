package bita

import (
	"bytes"
	"testing"
)

func TestCompressionRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("the quick brown fox "), 64)
	for _, algo := range []int32{compBrotli, compZstd} {
		c := Compression{algorithm: algo, level: 6}
		compressed := c.compress(data)
		out, err := decompressChunk(compressed, algo, len(data))
		if err != nil {
			t.Fatalf("algo %d decompress: %v", algo, err)
		}
		if !bytes.Equal(out, data) {
			t.Fatalf("algo %d roundtrip mismatch", algo)
		}
	}
}

func TestCompressionNonePassthrough(t *testing.T) {
	data := []byte("hello")
	c := Compression{algorithm: compNone}
	if got := c.compress(data); !bytes.Equal(got, data) {
		t.Fatalf("none compress = %q", got)
	}
}

func TestCompressionToDict(t *testing.T) {
	if d := (Compression{algorithm: compNone}).toDict(); d.compression != compNone || d.compressionLevel != 0 {
		t.Fatalf("none toDict = %+v", d)
	}
	if d := (Compression{algorithm: compBrotli, level: 9}).toDict(); d.compression != compBrotli || d.compressionLevel != 9 {
		t.Fatalf("brotli toDict = %+v", d)
	}
}

func TestDecompressErrors(t *testing.T) {
	if _, err := decompressChunk(nil, compLZMA, 0); err == nil {
		t.Fatal("lzma: expected error")
	}
	if _, err := decompressChunk(nil, 99, 0); err == nil {
		t.Fatal("unknown: expected error")
	}
	garbage := bytes.Repeat([]byte{0xff}, 32)
	if _, err := decompressChunk(garbage, compBrotli, 16); err == nil {
		t.Fatal("brotli garbage: expected error")
	}
	if _, err := decompressChunk([]byte{0x01, 0x02, 0x03, 0x04}, compZstd, 16); err == nil {
		t.Fatal("zstd garbage: expected error")
	}
}
