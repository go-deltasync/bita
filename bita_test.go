package bita_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/go-deltasync/bita"
)

// memTarget is an in-memory CloneTarget for the façade round-trip test.
type memTarget struct{ buf []byte }

func (m *memTarget) WriteAt(p []byte, off int64) (int, error) {
	if end := int(off) + len(p); end > len(m.buf) {
		m.buf = append(m.buf, make([]byte, end-len(m.buf))...)
	}
	copy(m.buf[off:], p)
	return len(p), nil
}

func (m *memTarget) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.buf)) {
		return 0, io.EOF
	}
	return copy(p, m.buf[off:]), nil
}

// TestFacadeRoundTrip exercises the public API: compress to an archive, open it,
// and clone it back (using a seed) through the exported package.
func TestFacadeRoundTrip(t *testing.T) {
	src := bytes.Repeat([]byte("go-deltasync bita public facade test payload "), 4096)
	var arc bytes.Buffer
	if err := bita.Compress(bytes.NewReader(src), &arc, bita.CompressConfig{Algorithm: bita.AlgoRollSum, Compression: bita.CompZstd}); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	a, err := bita.OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
	if err != nil {
		t.Fatalf("OpenArchiveReaderAt: %v", err)
	}
	if a.Info().SourceSize != uint64(len(src)) {
		t.Fatalf("info source size = %d, want %d", a.Info().SourceSize, len(src))
	}
	out := &memTarget{}
	if _, err := bita.Clone(a, out, bita.CloneOptions{Seeds: []io.Reader{bytes.NewReader(src)}, VerifyOutput: true}); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if !bytes.Equal(out.buf, src) {
		t.Fatal("façade clone output mismatch")
	}
}
