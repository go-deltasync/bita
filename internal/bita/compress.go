package bita

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// zstd encoder/decoder singletons. Construction with default options is
// infallible in practice; the discarded errors keep the package free of
// unreachable error branches (see org coverage rule). EncodeAll/DecodeAll are
// safe for concurrent use, so these are shared across worker goroutines.
var (
	zstdEncoder, _ = zstd.NewWriter(nil)
	zstdDecoder, _ = zstd.NewReader(nil)
	// zstdProbe is a fastest-level encoder used only as a cheap
	// incompressibility predictor (see Compression.skipCompression).
	zstdProbe, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
)

// Compression describes how chunk data is compressed.
type Compression struct {
	algorithm int32
	level     uint32
}

// brotliPools holds a reusable pool of brotli writers per compression level.
// A fresh brotli.Writer lazily allocates an 8 MiB ring buffer (and hash tables)
// for any chunk >= the 64 KiB block size, so creating one per chunk is very
// wasteful; brotli.Writer.Reset lets us reuse those buffers across chunks. The
// pool is safe for the concurrent per-chunk compression in Compress.
var brotliPools sync.Map // level int -> *sync.Pool of *brotli.Writer

func brotliCompress(data []byte, level int) []byte {
	pv, ok := brotliPools.Load(level)
	if !ok {
		pv, _ = brotliPools.LoadOrStore(level, &sync.Pool{
			New: func() any { return brotli.NewWriterLevel(io.Discard, level) },
		})
	}
	pool := pv.(*sync.Pool)
	w := pool.Get().(*brotli.Writer)
	var buf bytes.Buffer
	w.Reset(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	pool.Put(w)
	return buf.Bytes()
}

// compress returns the compressed form of data. Compression to an in-memory
// buffer is infallible for the algorithms we support, so no error is returned.
func (c Compression) compress(data []byte) []byte {
	switch c.algorithm {
	case compBrotli:
		return brotliCompress(data, int(c.level))
	case compZstd:
		return zstdEncoder.EncodeAll(data, nil)
	default: // compNone
		return data
	}
}

// skipCompression reports whether brotli should be skipped for this chunk. We
// run a very fast zstd probe: if even that finds no redundancy (cannot shrink
// the chunk at all), the data is incompressible and the far more expensive
// brotli pass would only end up discarded by the "store uncompressed when not
// smaller" rule — so we skip it. The probe is LZ-based, so genuinely redundant
// chunks (e.g. internal repeats) are NOT skipped; it only fires on data with no
// exploitable redundancy. Only relevant for brotli (zstd is its own encoder;
// none does not compress).
func (c Compression) skipCompression(data []byte) bool {
	if c.algorithm != compBrotli {
		return false
	}
	return len(zstdProbe.EncodeAll(data, nil)) >= len(data)
}

// toDict converts the compression into its dictionary message.
func (c Compression) toDict() *chunkCompression {
	if c.algorithm == compNone {
		return &chunkCompression{compression: compNone, compressionLevel: 0}
	}
	return &chunkCompression{compression: c.algorithm, compressionLevel: c.level}
}

// decompressChunk decompresses a stored chunk. It is only called for chunks
// that were actually compressed (archive_size != source_size), so there is no
// "none" case here.
func decompressChunk(data []byte, algorithm int32, sourceSize int) ([]byte, error) {
	switch algorithm {
	case compBrotli:
		out, err := io.ReadAll(brotli.NewReader(bytes.NewReader(data)))
		if err != nil {
			return nil, fmt.Errorf("bita: brotli decompress: %w", err)
		}
		return out, nil
	case compZstd:
		out, err := zstdDecoder.DecodeAll(data, make([]byte, 0, sourceSize))
		if err != nil {
			return nil, fmt.Errorf("bita: zstd decompress: %w", err)
		}
		return out, nil
	case compLZMA:
		return nil, errors.New("bita: lzma compression is not supported")
	default:
		return nil, fmt.Errorf("bita: unknown compression %d", algorithm)
	}
}
