package bita

import (
	"bytes"
	"encoding/binary"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func sampleDict() []byte {
	return (&chunkDictionary{
		chunkerParams:    &chunkerParameters{chunkingAlgorithm: algoRollSum, chunkHashLength: 64},
		chunkCompression: &chunkCompression{compression: compBrotli, compressionLevel: 6},
	}).marshal()
}

func TestHeaderRoundTrip(t *testing.T) {
	dict := sampleDict()
	h := buildHeader(dict, nil)
	parsed, err := parseHeader(newIOReader(bytes.NewReader(h)))
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if parsed.chunkDataOffset != uint64(len(h)) {
		t.Fatalf("chunkDataOffset = %d, want %d", parsed.chunkDataOffset, len(h))
	}
	if parsed.dictionary.chunkerParams.chunkingAlgorithm != algoRollSum {
		t.Fatal("dictionary not parsed")
	}
	if len(parsed.checksum) != headerHashLen {
		t.Fatalf("checksum len = %d", len(parsed.checksum))
	}
}

func TestHeaderExplicitOffset(t *testing.T) {
	dict := sampleDict()
	off := uint64(9999)
	h := buildHeader(dict, &off)
	parsed, err := parseHeader(newIOReader(bytes.NewReader(h)))
	if err != nil {
		t.Fatalf("parseHeader: %v", err)
	}
	if parsed.chunkDataOffset != off {
		t.Fatalf("chunkDataOffset = %d, want %d", parsed.chunkDataOffset, off)
	}
}

func TestHeaderLegacyMagic(t *testing.T) {
	dict := sampleDict()
	// Assemble a header by hand using the legacy magic.
	var body []byte
	body = append(body, legacyMagic...)
	body = binary.LittleEndian.AppendUint64(body, uint64(len(dict)))
	body = append(body, dict...)
	body = binary.LittleEndian.AppendUint64(body, uint64(len(body))+8+headerHashLen)
	sum := blake2b.Sum512(body)
	body = append(body, sum[:]...)
	if _, err := parseHeader(newIOReader(bytes.NewReader(body))); err != nil {
		t.Fatalf("legacy magic parse: %v", err)
	}
}

func TestHeaderBadMagic(t *testing.T) {
	h := buildHeader(sampleDict(), nil)
	h[0] = 'X'
	if _, err := parseHeader(newIOReader(bytes.NewReader(h))); err == nil {
		t.Fatal("expected bad magic error")
	}
}

func TestHeaderChecksumMismatch(t *testing.T) {
	h := buildHeader(sampleDict(), nil)
	h[len(h)-1] ^= 0xff
	if _, err := parseHeader(newIOReader(bytes.NewReader(h))); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestHeaderShortReads(t *testing.T) {
	// Too short for the pre-header.
	if _, err := parseHeader(newIOReader(bytes.NewReader([]byte{1, 2, 3}))); err == nil {
		t.Fatal("expected pre-header read error")
	}
	// Valid pre-header but truncated body.
	dict := sampleDict()
	var pre []byte
	pre = append(pre, archiveMagic...)
	pre = binary.LittleEndian.AppendUint64(pre, uint64(len(dict)))
	// no dictionary/offset/checksum following
	if _, err := parseHeader(newIOReader(bytes.NewReader(pre))); err == nil {
		t.Fatal("expected body read error")
	}
}

func TestHeaderBadDictionary(t *testing.T) {
	// Build a header whose dictionary bytes are invalid but checksum matches.
	badDict := []byte{0x80} // truncated varint => unmarshal error
	h := buildHeader(badDict, nil)
	if _, err := parseHeader(newIOReader(bytes.NewReader(h))); err == nil {
		t.Fatal("expected dictionary decode error")
	}
}

func TestHeaderMissingChunkerParams(t *testing.T) {
	// A valid dictionary with no chunker params fails validate().
	dict := (&chunkDictionary{}).marshal()
	h := buildHeader(dict, nil)
	if _, err := parseHeader(newIOReader(bytes.NewReader(h))); err == nil {
		t.Fatal("expected validate error")
	}
}
