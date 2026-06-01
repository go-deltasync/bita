package bita

import (
	"bytes"
	"testing"
)

func TestDictionaryRoundTrip(t *testing.T) {
	orig := &chunkDictionary{
		applicationVersion: "test-app",
		sourceChecksum:     []byte{1, 2, 3, 4},
		sourceTotalSize:    123456,
		chunkerParams: &chunkerParameters{
			chunkFilterBits:       15,
			minChunkSize:          16384,
			maxChunkSize:          1 << 24,
			rollingHashWindowSize: 64,
			chunkHashLength:       64,
			chunkingAlgorithm:     algoRollSum,
		},
		chunkCompression: &chunkCompression{compression: compBrotli, compressionLevel: 6},
		rebuildOrder:     []uint32{0, 1, 0, 2},
		chunkDescriptors: []chunkDescriptor{
			{checksum: []byte{9}, archiveSize: 10, archiveOffset: 0, sourceSize: 20},
			{checksum: []byte{8}, archiveSize: 5, archiveOffset: 10, sourceSize: 7},
			{checksum: []byte{7}, archiveSize: 3, archiveOffset: 15, sourceSize: 4},
		},
		metadata: map[string][]byte{"k": []byte("v"), "empty": {}},
	}
	got, err := unmarshalDictionary(orig.marshal())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.applicationVersion != "test-app" || string(got.sourceChecksum) != "\x01\x02\x03\x04" {
		t.Fatalf("scalar fields wrong: %+v", got)
	}
	if got.sourceTotalSize != 123456 {
		t.Fatalf("sourceTotalSize = %d", got.sourceTotalSize)
	}
	if *got.chunkerParams != *orig.chunkerParams {
		t.Fatalf("chunkerParams = %+v", got.chunkerParams)
	}
	if *got.chunkCompression != *orig.chunkCompression {
		t.Fatalf("chunkCompression = %+v", got.chunkCompression)
	}
	if len(got.rebuildOrder) != 4 || got.rebuildOrder[3] != 2 {
		t.Fatalf("rebuildOrder = %v", got.rebuildOrder)
	}
	if len(got.chunkDescriptors) != 3 || got.chunkDescriptors[1].archiveOffset != 10 {
		t.Fatalf("descriptors = %+v", got.chunkDescriptors)
	}
	if string(got.metadata["k"]) != "v" {
		t.Fatalf("metadata k = %q", got.metadata["k"])
	}
	if len(got.metadata["empty"]) != 0 {
		t.Fatalf("metadata empty = %q", got.metadata["empty"])
	}
}

func TestDictionaryMinimalFixedSize(t *testing.T) {
	orig := &chunkDictionary{
		chunkerParams:    &chunkerParameters{maxChunkSize: 4096, chunkHashLength: 32, chunkingAlgorithm: algoFixedSize},
		chunkCompression: &chunkCompression{compression: compNone},
	}
	got, err := unmarshalDictionary(orig.marshal())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.chunkerParams.chunkingAlgorithm != algoFixedSize || got.chunkerParams.maxChunkSize != 4096 {
		t.Fatalf("fixed params = %+v", got.chunkerParams)
	}
	if got.chunkCompression.compression != compNone {
		t.Fatalf("compression = %+v", got.chunkCompression)
	}
	if len(got.rebuildOrder) != 0 || len(got.chunkDescriptors) != 0 {
		t.Fatalf("expected empty repeated fields")
	}
}

func TestUnmarshalDictionaryUnknownFieldSkipped(t *testing.T) {
	b := (&chunkDictionary{
		chunkerParams: &chunkerParameters{chunkingAlgorithm: algoRollSum},
	}).marshal()
	// Append an unknown field 99 (varint) and an unknown len field 100.
	b = appendVarintField(b, 99, 7)
	b = appendBytesField(b, 100, []byte("ignored"))
	got, err := unmarshalDictionary(b)
	if err != nil {
		t.Fatalf("unmarshal with unknown fields: %v", err)
	}
	if got.chunkerParams == nil {
		t.Fatal("lost known field")
	}
}

func TestUnmarshalDictionaryNonPackedRebuildOrder(t *testing.T) {
	// rebuild_order field 6 encoded as individual varints (non-packed).
	var b []byte
	b = appendVarintField(b, 6, 3)
	b = appendVarintField(b, 6, 5)
	got, err := unmarshalDictionary(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.rebuildOrder) != 2 || got.rebuildOrder[0] != 3 || got.rebuildOrder[1] != 5 {
		t.Fatalf("rebuildOrder = %v", got.rebuildOrder)
	}
}

func TestUnmarshalDictionaryErrors(t *testing.T) {
	// Truncated top-level field (tag with no value).
	if _, err := unmarshalDictionary([]byte{0x18}); err == nil { // field 3 varint, no body
		t.Fatal("expected error from truncated dict")
	}
	badSub := []byte{0x08} // field 1 varint, truncated
	cases := map[string][]byte{
		"chunker_params":   appendMessageField(nil, 4, badSub),
		"chunk_compression": appendMessageField(nil, 5, badSub),
		"chunk_descriptor": appendMessageField(nil, 7, badSub),
		"map_entry":        appendMessageField(nil, 8, badSub),
	}
	for name, b := range cases {
		if _, err := unmarshalDictionary(b); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
	// Packed rebuild_order with a truncated body.
	bad := appendTag(nil, 6, wireLen)
	bad = append(bad, 1, 0x80) // length 1, body is a lone continuation byte
	if _, err := unmarshalDictionary(bad); err == nil {
		t.Fatal("expected error from truncated packed rebuild_order")
	}
}

func TestDictionaryValidate(t *testing.T) {
	if err := (&chunkDictionary{}).validate(); err == nil {
		t.Fatal("expected validate error for missing chunker params")
	}
	if err := (&chunkDictionary{chunkerParams: &chunkerParameters{}}).validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestSubMessageTagError(t *testing.T) {
	// Directly exercise the readTag error path in a sub-message unmarshaler.
	if _, err := unmarshalChunkerParameters([]byte{0x80}); err == nil {
		t.Fatal("expected tag error")
	}
	if _, err := unmarshalChunkCompression([]byte{0x80}); err == nil {
		t.Fatal("expected tag error")
	}
	if _, err := unmarshalChunkDescriptor([]byte{0x80}); err == nil {
		t.Fatal("expected tag error")
	}
	if _, _, err := unmarshalMapEntry([]byte{0x80}); err == nil {
		t.Fatal("expected tag error")
	}
	// Value error path in each sub-message unmarshaler (valid tag, no value).
	if _, err := unmarshalChunkerParameters([]byte{0x08}); err == nil {
		t.Fatal("expected value error")
	}
	if _, err := unmarshalChunkCompression([]byte{0x10}); err == nil {
		t.Fatal("expected value error")
	}
	if _, err := unmarshalChunkDescriptor([]byte{0x08}); err == nil {
		t.Fatal("expected value error")
	}
	if _, _, err := unmarshalMapEntry([]byte{0x0a}); err == nil {
		t.Fatal("expected value error")
	}
}

func TestMapEntryKeyAndUnknownField(t *testing.T) {
	var entry []byte
	entry = appendBytesField(entry, 1, []byte("key"))
	entry = appendBytesField(entry, 2, []byte("val"))
	entry = appendVarintField(entry, 9, 1) // unknown field, skipped
	k, v, err := unmarshalMapEntry(entry)
	if err != nil || k != "key" || string(v) != "val" {
		t.Fatalf("map entry = %q/%q, %v", k, v, err)
	}
}

func ensureMarshalDeterministic(t *testing.T) {
	d := &chunkDictionary{metadata: map[string][]byte{"b": []byte("2"), "a": []byte("1")}}
	if !bytes.Equal(d.marshal(), d.marshal()) {
		t.Fatal("marshal not deterministic")
	}
}

func TestMarshalDeterministic(t *testing.T) { ensureMarshalDeterministic(t) }
