package bita

import (
	"io"
	"math/bits"
)

// refillSize is how many bytes the streaming chunker reads per refill, matching
// bita's REFILL_SIZE.
const refillSize = 1024 * 1024

// rollingHash is the common interface for RollSum and BuzHash.
type rollingHash interface {
	// init feeds a byte during the window-priming phase (BuzHash only).
	init(b byte)
	// initDone reports whether the priming phase is complete.
	initDone() bool
	// input feeds a byte during steady-state rolling.
	input(b byte)
	// sum returns the current rolling hash value.
	sum() uint32
}

// chunkerConfig describes how to split a stream into chunks.
type chunkerConfig struct {
	algorithm    int32
	filterBits   uint32
	minChunkSize int
	maxChunkSize int
	windowSize   int
	fixedSize    int
}

// filterBitsFromSize converts an average target chunk size into a filter-bits
// value (bita's FilterBits::from_size).
func filterBitsFromSize(size uint32) uint32 {
	return 30 - uint32(bits.LeadingZeros32(size))
}

// filterMask returns the boundary bit mask for a filter-bits value.
func filterMask(fb uint32) uint32 {
	return uint32(0xffffffff) >> (32 - fb)
}

func saturatingSub(a, b int) int {
	if b > a {
		return 0
	}
	return a - b
}

// chunker is a streaming content-defined chunker mirroring bita's
// RollingHashChunker + StreamingChunker (and FixedSizeChunker).
type chunker struct {
	r          io.Reader
	buf        []byte
	base       int // start of unconsumed data within buf
	chunkStart uint64
	done       bool

	fixed     bool
	fixedSize int

	hasher         rollingHash
	filterMask     uint32
	minChunkSize   int
	maxChunkSize   int
	hashInputLimit int
	offset         int // scan offset relative to base (bita's self.offset)
}

func newChunker(r io.Reader, cfg chunkerConfig) *chunker {
	c := &chunker{r: r}
	if cfg.algorithm == algoFixedSize {
		c.fixed = true
		c.fixedSize = cfg.fixedSize
		return c
	}
	if cfg.algorithm == algoBuzHash {
		c.hasher = newBuzHash(cfg.windowSize)
	} else {
		c.hasher = newRollSum(cfg.windowSize)
	}
	c.filterMask = filterMask(cfg.filterBits)
	c.minChunkSize = cfg.minChunkSize
	c.maxChunkSize = cfg.maxChunkSize
	c.hashInputLimit = saturatingSub(cfg.minChunkSize, cfg.windowSize)
	return c
}

// next returns the next chunk as (sourceOffset, data). It returns io.EOF when
// the stream is exhausted.
func (c *chunker) next() (uint64, []byte, error) {
	for {
		if c.base < len(c.buf) {
			if n, ok := c.scanOne(); ok {
				off, data := c.emit(n)
				return off, data, nil
			}
		}
		if c.done {
			return 0, nil, io.EOF
		}
		read, err := c.refill()
		if err != nil {
			return 0, nil, err
		}
		if read == 0 {
			c.done = true
			if c.base < len(c.buf) {
				off, data := c.emit(len(c.buf) - c.base)
				return off, data, nil
			}
			return 0, nil, io.EOF
		}
	}
}

// emit slices n bytes out of the unconsumed buffer as a chunk.
func (c *chunker) emit(n int) (uint64, []byte) {
	data := append([]byte(nil), c.buf[c.base:c.base+n]...)
	c.base += n
	c.offset = 0
	off := c.chunkStart
	c.chunkStart += uint64(n)
	return off, data
}

// refill compacts the consumed prefix and reads up to refillSize more bytes.
func (c *chunker) refill() (int, error) {
	if c.base > 0 {
		n := copy(c.buf, c.buf[c.base:])
		c.buf = c.buf[:n]
		c.base = 0
	}
	start := len(c.buf)
	if cap(c.buf) < start+refillSize {
		nb := make([]byte, start, start+refillSize)
		copy(nb, c.buf)
		c.buf = nb
	}
	c.buf = c.buf[:start+refillSize]
	n, err := c.r.Read(c.buf[start:])
	c.buf = c.buf[:start+n]
	if err == io.EOF {
		err = nil
	}
	return n, err
}

// scanOne tries to find the next chunk boundary in the unconsumed buffer,
// returning (chunkLen, true) if one is found.
func (c *chunker) scanOne() (int, bool) {
	if c.fixed {
		if len(c.buf)-c.base >= c.fixedSize {
			return c.fixedSize, true
		}
		return 0, false
	}
	buf := c.buf[c.base:]
	// Prime the hasher's window (BuzHash); RollSum's initDone is always true.
	for !c.hasher.initDone() && c.offset < len(buf) {
		c.hasher.init(buf[c.offset])
		c.offset++
	}
	// Skip past the minimum chunk size, only priming the rolling window.
	if c.hashInputLimit > 0 && c.offset < c.hashInputLimit {
		c.offset = min(c.hashInputLimit-1, len(buf))
	}
	if c.minChunkSize > 0 && c.offset < c.minChunkSize {
		end := min(c.minChunkSize-1, len(buf))
		for c.offset < end {
			c.hasher.input(buf[c.offset])
			c.offset++
		}
		c.offset = end
	}
	// Scan for a boundary up to the max chunk size or end of buffer.
	minBytes := min(c.maxChunkSize, len(buf))
	for c.offset < minBytes {
		c.hasher.input(buf[c.offset])
		c.offset++
		if s := c.hasher.sum(); s|c.filterMask == s {
			return c.offset, true
		}
	}
	if c.offset >= c.maxChunkSize {
		return c.offset, true
	}
	return 0, false
}
