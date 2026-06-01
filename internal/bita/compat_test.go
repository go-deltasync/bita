//go:build compat

// Cross-implementation compatibility tests against the reference Rust `bita`
// CLI (https://github.com/oll3/bita). They are skipped when `bita` is not on
// PATH, so the suite stays green where the reference is unavailable. Run with:
//
//	go test -tags=compat ./internal/bita/...
package bita

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireBita(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bita")
	if err != nil {
		t.Skip("reference `bita` CLI not found on PATH; skipping compat test")
	}
	return path
}

// compatData builds a payload with enough structure that the content-defined
// chunker produces several chunks.
func compatData(seed int64, n int) []byte {
	rng := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	rng.Read(b)
	// Insert a few long runs so chunk boundaries are exercised.
	for i := 0; i < n; i += 50000 {
		end := i + 4096
		if end > n {
			end = n
		}
		for j := i; j < end; j++ {
			b[j] = byte(i / 50000)
		}
	}
	return b
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestCompatGoCompressRustClone: our Compress output is cloned back by the Rust
// bita CLI and must reproduce the source byte-for-byte.
func TestCompatGoCompressRustClone(t *testing.T) {
	bin := requireBita(t)
	for _, algo := range []string{AlgoRollSum, AlgoBuzHash} {
		t.Run(algo, func(t *testing.T) {
			dir := t.TempDir()
			src := compatData(1, 512*1024)
			archive := filepath.Join(dir, "a.cba")

			var buf bytes.Buffer
			if err := Compress(bytes.NewReader(src), &buf, CompressConfig{Algorithm: algo, Compression: CompBrotli}); err != nil {
				t.Fatalf("go compress: %v", err)
			}
			writeFile(t, archive, buf.Bytes())

			out := filepath.Join(dir, "out")
			cmd := exec.Command(bin, "clone", "--force-create", archive, out)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rust bita clone: %v\n%s", err, output)
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("read output: %v", err)
			}
			if !bytes.Equal(got, src) {
				t.Fatal("rust clone of go archive mismatched source")
			}
		})
	}
}

// TestCompatRustCompressGoClone: the Rust bita CLI produces an archive that our
// Clone reconstructs (fetching everything from the archive).
func TestCompatRustCompressGoClone(t *testing.T) {
	bin := requireBita(t)
	for _, algo := range []string{"RollSum", "BuzHash"} {
		t.Run(algo, func(t *testing.T) {
			dir := t.TempDir()
			src := compatData(2, 512*1024)
			input := filepath.Join(dir, "in")
			archive := filepath.Join(dir, "a.cba")
			writeFile(t, input, src)

			cmd := exec.Command(bin, "compress", "-i", input, archive,
				"--force-create", "--hash-chunking", algo, "--compression", "brotli")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rust bita compress: %v\n%s", err, output)
			}

			f, err := os.Open(archive)
			if err != nil {
				t.Fatalf("open archive: %v", err)
			}
			defer func() { _ = f.Close() }()
			a, err := OpenArchiveReaderAt(f)
			if err != nil {
				t.Fatalf("go open: %v", err)
			}
			out := newMemTarget()
			if _, err := Clone(a, out, CloneOptions{VerifyOutput: true}); err != nil {
				t.Fatalf("go clone: %v", err)
			}
			if !bytes.Equal(out.bytes(), src) {
				t.Fatal("go clone of rust archive mismatched source")
			}
		})
	}
}

// TestCompatChunkerBoundaries proves our chunker yields the same boundaries (and
// thus the same chunk hashes) as the Rust implementation: a Rust-built archive
// cloned with the exact source as seed must fetch nothing from the archive.
func TestCompatChunkerBoundaries(t *testing.T) {
	bin := requireBita(t)
	for _, algo := range []string{"RollSum", "BuzHash"} {
		t.Run(algo, func(t *testing.T) {
			dir := t.TempDir()
			src := compatData(3, 512*1024)
			input := filepath.Join(dir, "in")
			archive := filepath.Join(dir, "a.cba")
			writeFile(t, input, src)

			cmd := exec.Command(bin, "compress", "-i", input, archive,
				"--force-create", "--hash-chunking", algo, "--compression", "brotli")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rust bita compress: %v\n%s", err, output)
			}

			f, err := os.Open(archive)
			if err != nil {
				t.Fatalf("open archive: %v", err)
			}
			defer func() { _ = f.Close() }()
			a, err := OpenArchiveReaderAt(f)
			if err != nil {
				t.Fatalf("go open: %v", err)
			}
			out := newMemTarget()
			stats, err := Clone(a, out, CloneOptions{Seeds: []io.Reader{bytes.NewReader(src)}, VerifyOutput: true})
			if err != nil {
				t.Fatalf("go clone with seed: %v", err)
			}
			if !bytes.Equal(out.bytes(), src) {
				t.Fatal("seeded clone mismatched source")
			}
			if stats.FromArchive != 0 {
				t.Fatalf("chunk boundaries differ from reference: %d bytes fetched from archive", stats.FromArchive)
			}
		})
	}
}
