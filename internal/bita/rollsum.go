package bita

// rollSum is a port of bita's RollSum rolling hash (rsync/bup style). See
// bitar/src/rolling_hash/rollsum.rs. All arithmetic is u32 wrapping, which Go
// provides natively for uint32.
type rollSum struct {
	s1, s2 uint32
	window []byte
	offset int
}

const charOffset uint32 = 31

func newRollSum(windowSize int) *rollSum {
	w := uint32(windowSize)
	return &rollSum{
		s1:     w * charOffset,
		s2:     w * (w - 1) * charOffset,
		window: make([]byte, windowSize),
	}
}

// init is part of the rollingHash interface. RollSum needs no priming phase
// (initDone is always true), so the chunker never calls this; it is a no-op
// kept for interface symmetry with BuzHash.
func (r *rollSum) init(b byte) { _ = b }

func (r *rollSum) initDone() bool { return true }

func (r *rollSum) input(b byte) {
	drop := r.window[r.offset]
	r.s1 += uint32(b) - uint32(drop)
	r.s2 += r.s1 - uint32(len(r.window))*(uint32(drop)+charOffset)
	r.window[r.offset] = b
	r.offset++
	if r.offset >= len(r.window) {
		r.offset = 0
	}
}

func (r *rollSum) sum() uint32 {
	return (r.s1 << 16) | (r.s2 & 0xffff)
}
