package bita

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// chunkAll drains a chunker into a slice of (offset, len) pairs.
func chunkAll(t *testing.T, r io.Reader, cfg chunkerConfig) (offsets []uint64, sizes []int, joined []byte) {
	t.Helper()
	c := newChunker(r, cfg)
	for {
		off, data, err := c.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("chunker: %v", err)
		}
		offsets = append(offsets, off)
		sizes = append(sizes, len(data))
		joined = append(joined, data...)
	}
	return
}

func rollCfg(algo int32, min, max, window int) chunkerConfig {
	return chunkerConfig{algorithm: algo, filterBits: 5, minChunkSize: min, maxChunkSize: max, windowSize: window}
}

func TestChunkerZeroData(t *testing.T) {
	for _, algo := range []int32{algoRollSum, algoBuzHash} {
		offs, _, joined := chunkAll(t, bytes.NewReader(nil), rollCfg(algo, 3, 640, 5))
		if len(offs) != 0 || len(joined) != 0 {
			t.Fatalf("algo %d: expected no chunks, got %d", algo, len(offs))
		}
	}
}

func TestChunkerSmallerThanWindow(t *testing.T) {
	src := []byte{0x1f, 0x55, 0x39, 0x5e, 0xfa}
	for _, algo := range []int32{algoRollSum, algoBuzHash} {
		offs, sizes, joined := chunkAll(t, bytes.NewReader(src), rollCfg(algo, 0, 40, 10))
		if len(offs) != 1 || sizes[0] != 5 || !bytes.Equal(joined, src) {
			t.Fatalf("algo %d: %v / %v", algo, offs, sizes)
		}
	}
}

func TestChunkerSmallerThanMin(t *testing.T) {
	src := []byte{0x1f, 0x55, 0x39, 0x5e, 0xfa}
	for _, algo := range []int32{algoRollSum, algoBuzHash} {
		offs, sizes, joined := chunkAll(t, bytes.NewReader(src), rollCfg(algo, 10, 40, 5))
		if len(offs) != 1 || sizes[0] != 5 || !bytes.Equal(joined, src) {
			t.Fatalf("algo %d: %v / %v", algo, offs, sizes)
		}
	}
}

func TestChunkerFixedSize(t *testing.T) {
	src := bytes.Repeat([]byte{0xAB}, 25)
	offs, sizes, joined := chunkAll(t, bytes.NewReader(src), chunkerConfig{algorithm: algoFixedSize, fixedSize: 10})
	if len(offs) != 3 || sizes[0] != 10 || sizes[1] != 10 || sizes[2] != 5 {
		t.Fatalf("fixed sizes = %v", sizes)
	}
	if offs[1] != 10 || offs[2] != 20 || !bytes.Equal(joined, src) {
		t.Fatalf("fixed offsets = %v", offs)
	}
}

func TestChunkerMaxBoundary(t *testing.T) {
	// A large filter mask makes boundaries rare so chunks hit max_chunk_size.
	cfg := chunkerConfig{algorithm: algoRollSum, filterBits: 31, minChunkSize: 0, maxChunkSize: 16, windowSize: 4}
	src := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 8) // 64 bytes
	_, sizes, joined := chunkAll(t, bytes.NewReader(src), cfg)
	if !bytes.Equal(joined, src) {
		t.Fatal("max-boundary join mismatch")
	}
	for i, s := range sizes {
		if i < len(sizes)-1 && s != 16 {
			t.Fatalf("chunk %d size %d, expected max 16", i, s)
		}
	}
}

// slowReader returns at most n bytes per Read, to exercise the refill loop and
// boundary resume across refills.
type slowReader struct {
	data []byte
	pos  int
	n    int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	k := s.n
	if k > len(p) {
		k = len(p)
	}
	if k > len(s.data)-s.pos {
		k = len(s.data) - s.pos
	}
	copy(p, s.data[s.pos:s.pos+k])
	s.pos += k
	return k, nil
}

func TestChunkerDripFeedEquivalence(t *testing.T) {
	src := make([]byte, 5000)
	seed := 0xa3
	for i := range src {
		seed ^= seed * 4
		src[i] = byte(seed ^ i)
	}
	for _, algo := range []int32{algoRollSum, algoBuzHash} {
		cfg := rollCfg(algo, 20, 600, 10)
		_, wantSizes, wantJoin := chunkAll(t, bytes.NewReader(src), cfg)
		_, gotSizes, gotJoin := chunkAll(t, &slowReader{data: src, n: 1}, cfg)
		if !bytes.Equal(wantJoin, gotJoin) || !bytes.Equal(wantJoin, src) {
			t.Fatalf("algo %d: join mismatch", algo)
		}
		if len(wantSizes) != len(gotSizes) {
			t.Fatalf("algo %d: chunk count %d vs %d", algo, len(wantSizes), len(gotSizes))
		}
		for i := range wantSizes {
			if wantSizes[i] != gotSizes[i] {
				t.Fatalf("algo %d: chunk %d size %d vs %d", algo, i, wantSizes[i], gotSizes[i])
			}
		}
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestChunkerReadError(t *testing.T) {
	want := errors.New("boom")
	c := newChunker(errReader{want}, rollCfg(algoRollSum, 20, 600, 10))
	if _, _, err := c.next(); !errors.Is(err, want) {
		t.Fatalf("chunker read error = %v", err)
	}
}

func TestSaturatingSub(t *testing.T) {
	if saturatingSub(10, 3) != 7 || saturatingSub(3, 10) != 0 {
		t.Fatal("saturatingSub")
	}
}

func TestFilterHelpers(t *testing.T) {
	if filterBitsFromSize(64*1024) != 15 {
		t.Fatalf("filterBitsFromSize = %d", filterBitsFromSize(64*1024))
	}
	if filterMask(3) != 0b111 {
		t.Fatalf("filterMask = %b", filterMask(3))
	}
}
