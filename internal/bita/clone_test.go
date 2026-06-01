package bita

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func craftArchive(d *chunkDictionary, chunkData []byte) []byte {
	return append(buildHeader(d.marshal(), nil), chunkData...)
}

func TestCloneWithSeed(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	v1 := make([]byte, 256*1024)
	rng.Read(v1)
	v2 := append([]byte(nil), v1...)
	copy(v2[130000:], bytes.Repeat([]byte{0x5a}, 4096)) // edit one region

	var arc bytes.Buffer
	if err := Compress(bytes.NewReader(v2), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, err := OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	out := newMemTarget()
	stats, err := Clone(a, out, CloneOptions{Seeds: []io.Reader{bytes.NewReader(v1)}, VerifyOutput: true})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !bytes.Equal(out.bytes(), v2) {
		t.Fatal("seed clone output mismatch")
	}
	if stats.FromSeed == 0 {
		t.Fatal("expected some bytes reused from seed")
	}
}

func TestCloneFullySeeded(t *testing.T) {
	// When the seed equals the source, every chunk is satisfied from the seed
	// and the archive-fetch stage is skipped entirely.
	src := bytes.Repeat([]byte("fully seeded payload "), 8192)
	var arc bytes.Buffer
	if err := Compress(bytes.NewReader(src), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, _ := OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
	out := newMemTarget()
	stats, err := Clone(a, out, CloneOptions{Seeds: []io.Reader{bytes.NewReader(src)}, VerifyOutput: true})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if stats.FromArchive != 0 {
		t.Fatalf("expected nothing from archive, got %d", stats.FromArchive)
	}
	if !bytes.Equal(out.bytes(), src) {
		t.Fatal("fully-seeded output mismatch")
	}
}

func TestCloneSeedChunkerError(t *testing.T) {
	var arc bytes.Buffer
	if err := Compress(bytes.NewReader([]byte("hello world")), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, _ := OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
	out := newMemTarget()
	_, err := Clone(a, out, CloneOptions{Seeds: []io.Reader{errReader{errBoom}}})
	if !errors.Is(err, errBoom) {
		t.Fatalf("seed chunker error = %v", err)
	}
}

type writeFailTarget struct{}

func (writeFailTarget) WriteAt([]byte, int64) (int, error) { return 0, errBoom }
func (writeFailTarget) ReadAt([]byte, int64) (int, error)  { return 0, errBoom }

func TestCloneFetchWriteError(t *testing.T) {
	var arc bytes.Buffer
	if err := Compress(bytes.NewReader([]byte("hello world")), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, _ := OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
	if _, err := Clone(a, writeFailTarget{}, CloneOptions{}); !errors.Is(err, errBoom) {
		t.Fatalf("fetch write error = %v", err)
	}
}

func TestCloneSeedWriteError(t *testing.T) {
	data := []byte("hello world, this is the seed and the source")
	var arc bytes.Buffer
	if err := Compress(bytes.NewReader(data), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, _ := OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
	// Seed equals the source, so the first chunk matches and the failing
	// WriteAt is hit in the seed stage.
	_, err := Clone(a, writeFailTarget{}, CloneOptions{Seeds: []io.Reader{bytes.NewReader(data)}})
	if !errors.Is(err, errBoom) {
		t.Fatalf("seed write error = %v", err)
	}
}

func TestCloneChunkChecksumMismatch(t *testing.T) {
	chunk := []byte("hello")
	wrong := make([]byte, 64)
	for i := range wrong {
		wrong[i] = 0xde
	}
	d := &chunkDictionary{
		sourceTotalSize:  uint64(len(chunk)),
		chunkerParams:    &chunkerParameters{chunkingAlgorithm: algoRollSum, chunkHashLength: 64, rollingHashWindowSize: 64},
		chunkCompression: &chunkCompression{compression: compNone},
		rebuildOrder:     []uint32{0},
		chunkDescriptors: []chunkDescriptor{{checksum: wrong, archiveSize: uint32(len(chunk)), archiveOffset: 0, sourceSize: uint32(len(chunk))}},
	}
	a, err := OpenArchiveReaderAt(bytes.NewReader(craftArchive(d, chunk)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := Clone(a, newMemTarget(), CloneOptions{}); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestCloneFetchDecompressError(t *testing.T) {
	// A descriptor claims the chunk is compressed (archive_size != source_size)
	// but the stored bytes are not valid brotli, so decompression fails.
	garbage := bytes.Repeat([]byte{0xff}, 3)
	d := &chunkDictionary{
		sourceTotalSize:  10,
		chunkerParams:    &chunkerParameters{chunkingAlgorithm: algoRollSum, chunkHashLength: 64, rollingHashWindowSize: 64},
		chunkCompression: &chunkCompression{compression: compBrotli, compressionLevel: 6},
		rebuildOrder:     []uint32{0},
		chunkDescriptors: []chunkDescriptor{{checksum: make([]byte, 64), archiveSize: 3, archiveOffset: 0, sourceSize: 10}},
	}
	a, err := OpenArchiveReaderAt(bytes.NewReader(craftArchive(d, garbage)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := Clone(a, newMemTarget(), CloneOptions{}); err == nil {
		t.Fatal("expected decompress error")
	}
}

func TestCloneVerifyOutputFailure(t *testing.T) {
	chunk := []byte("hello")
	sum := blake2b.Sum512(chunk)
	wrongSource := make([]byte, 64) // not the real source checksum
	d := &chunkDictionary{
		sourceTotalSize:  uint64(len(chunk)),
		sourceChecksum:   wrongSource,
		chunkerParams:    &chunkerParameters{chunkingAlgorithm: algoRollSum, chunkHashLength: 64, rollingHashWindowSize: 64},
		chunkCompression: &chunkCompression{compression: compNone},
		rebuildOrder:     []uint32{0},
		chunkDescriptors: []chunkDescriptor{{checksum: sum[:], archiveSize: uint32(len(chunk)), archiveOffset: 0, sourceSize: uint32(len(chunk))}},
	}
	a, err := OpenArchiveReaderAt(bytes.NewReader(craftArchive(d, chunk)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := Clone(a, newMemTarget(), CloneOptions{VerifyOutput: true}); err == nil {
		t.Fatal("expected output verification failure")
	}
}

type readFailTarget struct{ *memTarget }

func (readFailTarget) ReadAt([]byte, int64) (int, error) { return 0, errBoom }

func TestCloneVerifyOutputReadError(t *testing.T) {
	var arc bytes.Buffer
	if err := Compress(bytes.NewReader([]byte("hello world")), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, _ := OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
	out := readFailTarget{newMemTarget()}
	if _, err := Clone(a, out, CloneOptions{VerifyOutput: true}); !errors.Is(err, errBoom) {
		t.Fatalf("verify read error = %v", err)
	}
}

func TestCloneFetchReadError(t *testing.T) {
	var arc bytes.Buffer
	if err := Compress(bytes.NewReader([]byte("hello world")), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	full := arc.Bytes()
	// Open from the full archive, then drop the chunk data so fetch reads fail.
	a, err := OpenArchiveReaderAt(bytes.NewReader(full[:a_headerLen(t, full)]))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := Clone(a, newMemTarget(), CloneOptions{}); err == nil {
		t.Fatal("expected fetch read error")
	}
}

// a_headerLen returns the chunk-data offset (== header length) of an archive.
func a_headerLen(t *testing.T, archive []byte) uint64 {
	t.Helper()
	a, err := OpenArchiveReaderAt(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("open for header len: %v", err)
	}
	return a.chunkDataOffset
}

func TestSourceIndex(t *testing.T) {
	// Two positions referencing the same unique chunk (index 0) plus a unique
	// chunk (index 1): rebuild_order [0,1,0].
	d := &chunkDictionary{
		chunkerParams: &chunkerParameters{chunkingAlgorithm: algoRollSum, chunkHashLength: 1, rollingHashWindowSize: 64},
		rebuildOrder:  []uint32{0, 1, 0},
		chunkDescriptors: []chunkDescriptor{
			{checksum: []byte{0xaa}, sourceSize: 10},
			{checksum: []byte{0xbb}, sourceSize: 20},
		},
	}
	a := &Archive{dict: d, hashLength: 1}
	idx := a.sourceIndex()
	locA := idx[string([]byte{0xaa})]
	locB := idx[string([]byte{0xbb})]
	if locA == nil || len(locA.offsets) != 2 || locA.offsets[0] != 0 || locA.offsets[1] != 30 {
		t.Fatalf("locA = %+v", locA)
	}
	if locB == nil || len(locB.offsets) != 1 || locB.offsets[0] != 10 {
		t.Fatalf("locB = %+v", locB)
	}
}

func TestWriteChunkError(t *testing.T) {
	if err := writeChunk(writeFailTarget{}, []uint64{0}, []byte("x")); !errors.Is(err, errBoom) {
		t.Fatalf("writeChunk error = %v", err)
	}
}
