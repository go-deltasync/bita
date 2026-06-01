//go:build compat

// Comparative performance tests against the reference Rust `bita` CLI. Both
// tools are invoked as subprocesses on identical inputs, so timings are
// apples-to-apples (each pays process startup + file I/O). Headline metrics are
// archive size (compression quality, exact) and clone time with a near seed
// (the differential-sync fast path). Wall-clock is the best of several runs and
// is indicative. Run with:
//
//	go test -tags=compat -v -run Perf ./internal/bita/
package bita

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func perfBitaData() (v1, v2 []byte) {
	const size = 16 << 20
	v1 = make([]byte, size)
	x := uint64(0x243f6a8885a308d3)
	for i := range v1 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		v1[i] = byte(x)
	}
	v2 = append([]byte(nil), v1...)
	// Scattered overwrites so most chunks are reused from the seed.
	for k := 0; k < 64; k++ {
		off := (k*1009 + 5000) * (size / 70000)
		if off+32 < len(v2) {
			copy(v2[off:off+32], []byte("EDITED-REGION-PERF-BENCHMARK----"))
		}
	}
	return
}

func timeBest(t *testing.T, runs int, name string, args ...string) time.Duration {
	t.Helper()
	best := time.Duration(1) << 62
	for i := 0; i < runs; i++ {
		start := time.Now()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

func statSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestPerfVsRustBita(t *testing.T) {
	rustBin := requireBita(t)
	dir := t.TempDir()

	// Build our CLI so both implementations run as subprocesses (fair timing).
	ourBin := filepath.Join(dir, "bita")
	if out, err := exec.Command("go", "build", "-o", ourBin, "github.com/go-deltasync/bita/cmd/bita").CombinedOutput(); err != nil {
		t.Skipf("go build failed: %v\n%s", err, out)
	}

	v1, v2 := perfBitaData()
	v1Path := filepath.Join(dir, "v1")
	v2Path := filepath.Join(dir, "v2")
	if err := os.WriteFile(v1Path, v1, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2Path, v2, 0o644); err != nil {
		t.Fatal(err)
	}

	const runs = 3
	ourArc := filepath.Join(dir, "our.cba")
	rustArc := filepath.Join(dir, "rust.cba")
	ourOut := filepath.Join(dir, "our.out")
	rustOut := filepath.Join(dir, "rust.out")

	// Compress (default RollSum + brotli on both sides).
	ourComp := timeBest(t, runs, ourBin, "compress", "-i", v2Path, ourArc, "--force")
	rustComp := timeBest(t, runs, rustBin, "compress", "-i", v2Path, rustArc, "--force-create", "--compression", "brotli")

	// Clone with v1 as a near seed (the differential-sync fast path).
	ourClone := timeBest(t, runs, ourBin, "clone", "--force", "--seed", v1Path, ourArc, ourOut)
	rustClone := timeBest(t, runs, rustBin, "clone", "--force-create", "--seed", v1Path, rustArc, rustOut)

	// Both reconstructions must equal the source.
	for _, p := range []string{ourOut, rustOut} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(v2) {
			t.Fatalf("%s reconstruction mismatch", p)
		}
	}

	mb := float64(len(v2)) / (1 << 20)
	t.Logf("\n"+
		"comparative performance (source %.0f MiB, best of %d runs, subprocess wall-clock)\n"+
		"  %-10s %16s %16s %14s\n"+
		"  %-10s %16s %16s %14d\n"+
		"  %-10s %16s %16s %14d\n",
		mb, runs,
		"impl", "compress", "clone(seed)", "archive bytes",
		"go-bita", rate(ourComp, mb), rate(ourClone, mb), statSize(t, ourArc),
		"rust bita", rate(rustComp, mb), rate(rustClone, mb), statSize(t, rustArc),
	)
}

func rate(d time.Duration, mb float64) string {
	return fmt.Sprintf("%.0fms %.0fMB/s", float64(d.Milliseconds()), mb/d.Seconds())
}
