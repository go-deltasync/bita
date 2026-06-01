package bita

import "testing"

// equalSums verifies that two byte ranges sharing a suffix converge to the same
// rolling-hash sum once the window has rolled past the differing prefix.
func TestBuzHashEqualSumsForEqualRange(t *testing.T) {
	h := newBuzHash(8)
	data1 := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22}
	data2 := []byte{1, 99, 99, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22}
	var s1, s2 []uint32
	for _, v := range data1 {
		h.input(v)
		s1 = append(s1, h.sum())
	}
	for _, v := range data2 {
		h.input(v)
		s2 = append(s2, h.sum())
	}
	for i := 11; i < len(s1); i++ {
		if s1[i] != s2[i] {
			t.Fatalf("sum %d differs: %d vs %d", i, s1[i], s2[i])
		}
	}
}

func TestBuzHashLastValidVector(t *testing.T) {
	h := newBuzHash(5)
	var sums []uint32
	for _, v := range []byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 1, 2, 3, 4, 5} {
		if !h.initDone() {
			h.init(v)
		} else {
			h.input(v)
		}
		if h.initDone() {
			sums = append(sums, h.sum())
		}
	}
	if sums[9] != 1406929643 {
		t.Fatalf("last valid sum = %d", sums[9])
	}
}

func TestBuzHashRepeatedInput(t *testing.T) {
	// Feeding the same byte more than `window` times must not change the sum
	// (exercises the repeated-input short-circuit).
	h := newBuzHash(4)
	for i := 0; i < 4; i++ {
		h.input(0x77)
	}
	stable := h.sum()
	for i := 0; i < 10; i++ {
		h.input(0x77)
		if h.sum() != stable {
			t.Fatalf("sum changed on repeated input")
		}
	}
}

func TestRollSumEqualSumsForEqualRange(t *testing.T) {
	h := newRollSum(8)
	data1 := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	data2 := []byte{9, 9, 9, 9, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	var s1, s2 []uint32
	for _, v := range data1 {
		h.input(v)
		s1 = append(s1, h.sum())
	}
	for _, v := range data2 {
		h.input(v)
		s2 = append(s2, h.sum())
	}
	// Window is 8 and the prefixes differ through index 3, so sums converge
	// only once the window has fully rolled past it (index 3 + 8 = 11).
	for i := 11; i < len(s1); i++ {
		if s1[i] != s2[i] {
			t.Fatalf("rollsum %d differs: %d vs %d", i, s1[i], s2[i])
		}
	}
}

func TestRollSumInitIsNoOp(t *testing.T) {
	// RollSum has no priming phase; init must be a no-op and initDone true.
	h := newRollSum(4)
	if !h.initDone() {
		t.Fatal("rollsum initDone should be true")
	}
	before := h.sum()
	h.init(123)
	if h.sum() != before {
		t.Fatal("rollsum init changed the sum")
	}
}
