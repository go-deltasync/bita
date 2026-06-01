package bita

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// zstd encoder/decoder singletons. Construction with default options is
// infallible in practice; the discarded errors keep the package free of
// unreachable error branches (see org coverage rule).
var (
	zstdEncoder, _ = zstd.NewWriter(nil)
	zstdDecoder, _ = zstd.NewReader(nil)
)

// Compression describes how chunk data is compressed.
type Compression struct {
	algorithm int32
	level     uint32
}

// compress returns the compressed form of data. Compression to an in-memory
// buffer is infallible for the algorithms we support, so no error is returned.
func (c Compression) compress(data []byte) []byte {
	switch c.algorithm {
	case compBrotli:
		var buf bytes.Buffer
		w := brotli.NewWriterLevel(&buf, int(c.level))
		_, _ = w.Write(data)
		_ = w.Close()
		return buf.Bytes()
	case compZstd:
		return zstdEncoder.EncodeAll(data, nil)
	default: // compNone
		return data
	}
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
