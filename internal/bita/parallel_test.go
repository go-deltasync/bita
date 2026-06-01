package bita

import (
	"sync/atomic"
	"testing"
)

func TestParallelMap(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		workers int
	}{
		{"empty", 0, 4},
		{"clamp-workers-to-n", 1, 4}, // workers > n => sequential
		{"single-worker", 5, 1},      // workers < 2 => sequential
		{"parallel", 200, 4},         // genuine fan-out
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := make([]int, tc.n)
			var calls int64
			parallelMap(tc.n, tc.workers, func(i int) {
				atomic.AddInt64(&calls, 1)
				out[i] = i * i
			})
			if int(calls) != tc.n {
				t.Fatalf("fn called %d times, want %d", calls, tc.n)
			}
			for i := 0; i < tc.n; i++ {
				if out[i] != i*i {
					t.Fatalf("index %d = %d, want %d", i, out[i], i*i)
				}
			}
		})
	}
}
