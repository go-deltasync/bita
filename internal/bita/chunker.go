package bita

import (
	"io"

	"github.com/go-deltasync/chunk"
)

// The chunker is github.com/go-deltasync/chunk, which is where bita's two
// rolling hashes, their seed and table, and the scan that looks for a boundary
// now live. They were here, and nothing outside bita could reach them; a
// content-addressed store wanting the same cuts had to copy them, and this
// organisation already had three rolling hashes and exported none.
//
// What stays here is what is bita's rather than chunking's: the archive's
// algorithm enum, and its filter-bits field, which is a boundary bit count and
// is handed to the chunker as one so that reading an archive cuts where the
// archive says and not where a rounded average would.

// filterBitsFromSize converts an average target chunk size into a filter-bits
// value (bita's FilterBits::from_size), for the header of an archive being
// written.
func filterBitsFromSize(size uint32) uint32 { return uint32(chunk.BitsFromAverage(int(size))) }

// chunkerConfig describes how to split a stream into chunks. It is the archive's
// own description of it, which is why the algorithm is bita's enum.
type chunkerConfig struct {
	algorithm    int32
	filterBits   uint32
	minChunkSize int
	maxChunkSize int
	windowSize   int
	fixedSize    int
}

// chunker cuts a stream as an archive says it was cut, or as one being written
// asks for. The fixed-size case is here because it is four lines and needs no
// hash; everything else is the shared chunker.
type chunker struct {
	// Exactly one of these is set.
	cdc   *chunk.Chunker
	fixed *fixedChunker
}

func newChunker(r io.Reader, cfg chunkerConfig) *chunker {
	if cfg.algorithm == algoFixedSize {
		return &chunker{fixed: &fixedChunker{r: r, size: cfg.fixedSize}}
	}
	rolling := chunk.NewBuzHashRolling
	if cfg.algorithm != algoBuzHash {
		rolling = chunk.NewRollSumRolling
	}
	return &chunker{cdc: chunk.New(r, chunk.Config{
		Rolling: rolling,
		Window:  cfg.windowSize,
		Bits:    int(cfg.filterBits),
		Min:     cfg.minChunkSize,
		Max:     cfg.maxChunkSize,
	})}
}

// next returns the next chunk as (sourceOffset, data). It returns io.EOF when
// the stream is exhausted.
func (c *chunker) next() (uint64, []byte, error) {
	if c.fixed != nil {
		return c.fixed.next()
	}
	return c.cdc.Next()
}

// fixedChunker cuts every size bytes, which is bita's FixedSizeChunker.
type fixedChunker struct {
	r    io.Reader
	size int
	at   uint64
}

func (f *fixedChunker) next() (uint64, []byte, error) {
	buf := make([]byte, f.size)
	// A short read at the end of the stream is a chunk, so the error that comes
	// with it is not passed on; the call after this one reads nothing and
	// returns io.EOF. Nothing read at all is that call, and io.ReadFull reports
	// it as io.EOF rather than as an unexpected one.
	n, err := io.ReadFull(f.r, buf)
	if n == 0 {
		return 0, nil, err
	}
	at := f.at
	f.at += uint64(n)
	return at, buf[:n], nil
}
