package bita

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestBuzHashKnownVector(t *testing.T) {
	h := newBuzHash(5)
	var got uint32
	gotSet := false
	for _, v := range []byte{1, 2, 3, 4, 5} {
		if !h.initDone() {
			h.init(v)
		} else {
			h.input(v)
		}
		if h.initDone() && !gotSet {
			got = h.sum()
			gotSet = true
		}
	}
	if got != 1406929643 {
		t.Fatalf("buzhash first valid sum = %d, want 1406929643", got)
	}
}

func TestCompressCloneRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	src := make([]byte, 1<<20)
	rng.Read(src)
	// inject repetition so dedup fires
	copy(src[700000:], src[100000:200000])

	for _, algo := range []string{AlgoRollSum, AlgoBuzHash, AlgoFixed} {
		for _, comp := range []string{CompBrotli, CompZstd, CompNone} {
			var arc bytes.Buffer
			conf := CompressConfig{Algorithm: algo, Compression: comp}
			if err := Compress(bytes.NewReader(src), &arc, conf); err != nil {
				t.Fatalf("%s/%s compress: %v", algo, comp, err)
			}
			a, err := OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
			if err != nil {
				t.Fatalf("%s/%s open: %v", algo, comp, err)
			}
			out := newMemTarget()
			stats, err := Clone(a, out, CloneOptions{VerifyOutput: true})
			if err != nil {
				t.Fatalf("%s/%s clone: %v", algo, comp, err)
			}
			if !bytes.Equal(out.bytes(), src) {
				t.Fatalf("%s/%s output mismatch (len %d vs %d)", algo, comp, len(out.bytes()), len(src))
			}
			if stats.TotalSize != uint64(len(src)) {
				t.Fatalf("%s/%s total size %d", algo, comp, stats.TotalSize)
			}
		}
	}
}

// memTarget is an in-memory CloneTarget for tests.
type memTarget struct {
	buf []byte
}

func newMemTarget() *memTarget { return &memTarget{} }

func (m *memTarget) WriteAt(p []byte, off int64) (int, error) {
	end := int(off) + len(p)
	if end > len(m.buf) {
		m.buf = append(m.buf, make([]byte, end-len(m.buf))...)
	}
	copy(m.buf[off:], p)
	return len(p), nil
}

func (m *memTarget) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.buf)) {
		return 0, bytes.ErrTooLarge
	}
	n := copy(p, m.buf[off:])
	return n, nil
}

func (m *memTarget) bytes() []byte { return m.buf }
