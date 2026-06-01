package bita

import (
	"bytes"
	"errors"
	"testing"
)

func TestResolveDefaults(t *testing.T) {
	cfg, comp, hl, err := CompressConfig{}.resolve()
	if err != nil {
		t.Fatalf("resolve defaults: %v", err)
	}
	if cfg.algorithm != algoRollSum || cfg.windowSize != defaultRollWindow {
		t.Fatalf("default cfg = %+v", cfg)
	}
	if cfg.filterBits != 15 || cfg.minChunkSize != defaultMinChunkSize || cfg.maxChunkSize != defaultMaxChunkSize {
		t.Fatalf("default cfg sizes = %+v", cfg)
	}
	if comp.algorithm != compBrotli || comp.level != 6 || hl != 64 {
		t.Fatalf("default comp = %+v hl=%d", comp, hl)
	}
}

func TestResolveVariants(t *testing.T) {
	cfg, _, _, err := CompressConfig{Algorithm: AlgoBuzHash}.resolve()
	if err != nil || cfg.algorithm != algoBuzHash || cfg.windowSize != defaultBuzWindow {
		t.Fatalf("buzhash resolve = %+v, %v", cfg, err)
	}
	cfg, _, _, err = CompressConfig{Algorithm: AlgoFixed, FixedSize: 4096}.resolve()
	if err != nil || cfg.algorithm != algoFixedSize || cfg.fixedSize != 4096 {
		t.Fatalf("fixed resolve = %+v, %v", cfg, err)
	}
	cfg, _, _, _ = CompressConfig{Algorithm: AlgoFixed}.resolve()
	if cfg.fixedSize != defaultAvgChunkSize {
		t.Fatalf("fixed default size = %d", cfg.fixedSize)
	}
	cfg, _, _, _ = CompressConfig{Algorithm: AlgoRollSum, WindowSize: 32, AvgChunkSize: 1024, MinChunkSize: 100, MaxChunkSize: 5000}.resolve()
	if cfg.windowSize != 32 || cfg.minChunkSize != 100 || cfg.maxChunkSize != 5000 {
		t.Fatalf("explicit cfg = %+v", cfg)
	}
	_, comp, _, _ := CompressConfig{Compression: CompZstd, CompressionLevel: 3}.resolve()
	if comp.algorithm != compZstd || comp.level != 3 {
		t.Fatalf("zstd comp = %+v", comp)
	}
	_, comp, _, _ = CompressConfig{Compression: CompNone}.resolve()
	if comp.algorithm != compNone {
		t.Fatalf("none comp = %+v", comp)
	}
}

func TestResolveErrors(t *testing.T) {
	if _, _, _, err := (CompressConfig{HashLength: 65}).resolve(); err == nil {
		t.Fatal("expected hash length error")
	}
	if _, _, _, err := (CompressConfig{HashLength: -1}).resolve(); err == nil {
		t.Fatal("expected negative hash length error")
	}
	if _, _, _, err := (CompressConfig{Compression: "bogus"}).resolve(); err == nil {
		t.Fatal("expected compression error")
	}
	if _, _, _, err := (CompressConfig{Algorithm: "bogus"}).resolve(); err == nil {
		t.Fatal("expected algorithm error")
	}
}

func TestParamsFromConfig(t *testing.T) {
	p := paramsFromConfig(chunkerConfig{algorithm: algoFixedSize, fixedSize: 8192}, 32)
	if p.chunkingAlgorithm != algoFixedSize || p.maxChunkSize != 8192 || p.chunkHashLength != 32 {
		t.Fatalf("fixed params = %+v", p)
	}
	if p.minChunkSize != 0 || p.chunkFilterBits != 0 || p.rollingHashWindowSize != 0 {
		t.Fatalf("fixed params should zero rolling fields: %+v", p)
	}
	p = paramsFromConfig(chunkerConfig{algorithm: algoBuzHash, filterBits: 12, minChunkSize: 1, maxChunkSize: 2, windowSize: 16}, 64)
	if p.chunkingAlgorithm != algoBuzHash || p.chunkFilterBits != 12 || p.rollingHashWindowSize != 16 {
		t.Fatalf("rolling params = %+v", p)
	}
}

func TestConfigFromParams(t *testing.T) {
	if c, err := configFromParams(&chunkerParameters{chunkingAlgorithm: algoFixedSize, maxChunkSize: 99}); err != nil || c.fixedSize != 99 {
		t.Fatalf("fixed = %+v, %v", c, err)
	}
	if c, err := configFromParams(&chunkerParameters{chunkingAlgorithm: algoBuzHash, rollingHashWindowSize: 16}); err != nil || c.windowSize != 16 {
		t.Fatalf("buzhash = %+v, %v", c, err)
	}
	if _, err := configFromParams(&chunkerParameters{chunkingAlgorithm: 9}); err == nil {
		t.Fatal("expected unknown algorithm error")
	}
}

type failWriter struct {
	after int
	calls int
}

var errBoom = errors.New("boom")

func (f *failWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > f.after {
		return 0, errBoom
	}
	return len(p), nil
}

func TestCompressWriteErrors(t *testing.T) {
	if err := Compress(bytes.NewReader([]byte("hello")), &failWriter{after: 0}, CompressConfig{}); !errors.Is(err, errBoom) {
		t.Fatalf("header write error = %v", err)
	}
	if err := Compress(bytes.NewReader([]byte("hello")), &failWriter{after: 1}, CompressConfig{}); !errors.Is(err, errBoom) {
		t.Fatalf("chunk-data write error = %v", err)
	}
}

func TestCompressInvalidConfig(t *testing.T) {
	if err := Compress(bytes.NewReader(nil), &bytes.Buffer{}, CompressConfig{Compression: "nope"}); err == nil {
		t.Fatal("expected config error")
	}
}

func TestCompressChunkerError(t *testing.T) {
	if err := Compress(errReader{errBoom}, &bytes.Buffer{}, CompressConfig{}); !errors.Is(err, errBoom) {
		t.Fatalf("chunker error = %v", err)
	}
}

func archiveBytes(d *chunkDictionary) []byte { return buildHeader(d.marshal(), nil) }

func TestOpenArchiveErrors(t *testing.T) {
	base := func() *chunkDictionary {
		return &chunkDictionary{
			chunkerParams:    &chunkerParameters{chunkingAlgorithm: algoRollSum, chunkHashLength: 64},
			chunkCompression: &chunkCompression{compression: compBrotli, compressionLevel: 6},
		}
	}
	// bad header (truncated)
	if _, err := OpenArchiveReaderAt(bytes.NewReader([]byte{1, 2})); err == nil {
		t.Fatal("expected header error")
	}
	// unknown algorithm
	d := base()
	d.chunkerParams.chunkingAlgorithm = 7
	if _, err := OpenArchiveReaderAt(bytes.NewReader(archiveBytes(d))); err == nil {
		t.Fatal("expected algorithm error")
	}
	// invalid hash length
	d = base()
	d.chunkerParams.chunkHashLength = 0
	if _, err := OpenArchiveReaderAt(bytes.NewReader(archiveBytes(d))); err == nil {
		t.Fatal("expected hash length error")
	}
	// unknown compression
	d = base()
	d.chunkCompression.compression = 9
	if _, err := OpenArchiveReaderAt(bytes.NewReader(archiveBytes(d))); err == nil {
		t.Fatal("expected compression error")
	}
	// rebuild_order out of range
	d = base()
	d.rebuildOrder = []uint32{3}
	if _, err := OpenArchiveReaderAt(bytes.NewReader(archiveBytes(d))); err == nil {
		t.Fatal("expected rebuild_order error")
	}
}

func TestInfoAndVerifyHeader(t *testing.T) {
	src := bytes.Repeat([]byte("abcdefgh"), 4096)
	var buf bytes.Buffer
	if err := Compress(bytes.NewReader(src), &buf, CompressConfig{Algorithm: AlgoRollSum, Compression: CompZstd, Metadata: map[string][]byte{"name": []byte("demo")}}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, err := OpenArchiveReaderAt(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	info := a.Info()
	if info.Algorithm != AlgoRollSum || info.Compression != CompZstd {
		t.Fatalf("info = %+v", info)
	}
	// Also exercise compressionName for brotli via a separate archive.
	var bbuf bytes.Buffer
	if err := Compress(bytes.NewReader(src), &bbuf, CompressConfig{Compression: CompBrotli}); err != nil {
		t.Fatalf("brotli compress: %v", err)
	}
	ba, _ := OpenArchiveReaderAt(bytes.NewReader(bbuf.Bytes()))
	if ba.Info().Compression != CompBrotli {
		t.Fatalf("expected brotli, got %s", ba.Info().Compression)
	}
	if info.SourceSize != uint64(len(src)) || info.AvgChunkSize == 0 || info.WindowSize != 64 {
		t.Fatalf("info sizes = %+v", info)
	}
	if info.HashLength != 64 || info.SourceChunks == 0 || info.UniqueChunks == 0 {
		t.Fatalf("info chunks = %+v", info)
	}
	if string(info.Metadata["name"]) != "demo" || info.BuiltWith != appVersion {
		t.Fatalf("info meta = %+v", info)
	}
	// VerifyHeader round-trips against the archive's own checksum.
	if a.HeaderChecksumHex() == "" {
		t.Fatal("empty header checksum")
	}
	if err := a.VerifyHeader(a.HeaderChecksumHex()); err != nil {
		t.Fatalf("verify header: %v", err)
	}
	if err := a.VerifyHeader("00ff"); err == nil {
		t.Fatal("expected header mismatch")
	}
	if err := a.VerifyHeader("zz"); err == nil {
		t.Fatal("expected bad hex error")
	}
}

func TestInfoFixedAndNoneAndLZMA(t *testing.T) {
	// Fixed-size + none compression archive via Compress.
	var buf bytes.Buffer
	if err := Compress(bytes.NewReader(bytes.Repeat([]byte{1}, 100)), &buf, CompressConfig{Algorithm: AlgoFixed, FixedSize: 16, Compression: CompNone}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	a, err := OpenArchiveReaderAt(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	info := a.Info()
	if info.Algorithm != AlgoFixed || info.Compression != CompNone {
		t.Fatalf("fixed/none info = %+v", info)
	}
	if info.AvgChunkSize != 0 || info.CompressionLevel != 0 {
		t.Fatalf("fixed should have no avg/level: %+v", info)
	}
	// LZMA archive (crafted) opens and reports lzma.
	d := &chunkDictionary{
		chunkerParams:    &chunkerParameters{chunkingAlgorithm: algoBuzHash, chunkHashLength: 64, rollingHashWindowSize: 16},
		chunkCompression: &chunkCompression{compression: compLZMA, compressionLevel: 5},
	}
	a, err = OpenArchiveReaderAt(bytes.NewReader(archiveBytes(d)))
	if err != nil {
		t.Fatalf("open lzma: %v", err)
	}
	if a.Info().Compression != "lzma" {
		t.Fatalf("expected lzma, got %s", a.Info().Compression)
	}
}
