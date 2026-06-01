package bita

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIOReader(t *testing.T) {
	r := newIOReader(bytes.NewReader([]byte("0123456789")))
	got, err := r.readAt(2, 4)
	if err != nil || string(got) != "2345" {
		t.Fatalf("readAt = %q, %v", got, err)
	}
	// zero-size read
	if got, err := r.readAt(0, 0); err != nil || len(got) != 0 {
		t.Fatalf("zero read = %q, %v", got, err)
	}
	// read past end => EOF-derived error
	if _, err := r.readAt(8, 10); err == nil {
		t.Fatal("expected short read error")
	}
}

// shortReaderAt always returns one byte with a nil error, to exercise the
// "n < size && err == nil" path in ioReader.readAt.
type shortReaderAt struct{}

func (shortReaderAt) ReadAt(p []byte, _ int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = 'x'
	return 1, nil
}

func TestIOReaderShortNoError(t *testing.T) {
	r := newIOReader(shortReaderAt{})
	if _, err := r.readAt(0, 5); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short-no-error path = %v", err)
	}
}

func TestReadRangesCoalescing(t *testing.T) {
	data := []byte("ABCDEFGHIJ")
	cr := &countingReader{data: data}
	// Two adjacent ranges [0,3) and [3,2) should coalesce into one read.
	ranges := []chunkRange{{offset: 0, size: 3, index: 0}, {offset: 3, size: 2, index: 1}}
	out, err := readRanges(cr, ranges)
	if err != nil {
		t.Fatalf("readRanges: %v", err)
	}
	if string(out[0]) != "ABC" || string(out[1]) != "DE" {
		t.Fatalf("coalesced out = %q", out)
	}
	if cr.calls != 1 {
		t.Fatalf("expected 1 coalesced read, got %d", cr.calls)
	}
	// Non-adjacent ranges => two reads, results in input order.
	cr = &countingReader{data: data}
	ranges = []chunkRange{{offset: 6, size: 2, index: 0}, {offset: 0, size: 2, index: 1}}
	out, err = readRanges(cr, ranges)
	if err != nil {
		t.Fatalf("readRanges: %v", err)
	}
	if string(out[0]) != "GH" || string(out[1]) != "AB" || cr.calls != 2 {
		t.Fatalf("non-adjacent out = %q calls=%d", out, cr.calls)
	}
	// Empty input.
	if out, err := readRanges(cr, nil); err != nil || len(out) != 0 {
		t.Fatalf("empty readRanges = %v, %v", out, err)
	}
	// Error propagation.
	if _, err := readRanges(errArchiveReader{}, []chunkRange{{offset: 0, size: 1}}); !errors.Is(err, errBoom) {
		t.Fatalf("readRanges error = %v", err)
	}
}

type countingReader struct {
	data  []byte
	calls int
}

func (c *countingReader) readAt(off uint64, size int) ([]byte, error) {
	c.calls++
	return c.data[off : int(off)+size], nil
}

type errArchiveReader struct{}

func (errArchiveReader) readAt(uint64, int) ([]byte, error) { return nil, errBoom }

func TestHTTPReaderZeroSize(t *testing.T) {
	r := newHTTPReader("http://example.invalid", HTTPOptions{}.toInternal())
	if got, err := r.readAt(0, 0); err != nil || len(got) != 0 {
		t.Fatalf("zero read = %q, %v", got, err)
	}
}

func TestHTTPReaderPartialContent(t *testing.T) {
	body := []byte("the quick brown fox jumps")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	r := newHTTPReader(srv.URL, HTTPOptions{}.toInternal())
	got, err := r.readAt(4, 5)
	if err != nil || string(got) != "quick" {
		t.Fatalf("partial = %q, %v", got, err)
	}
}

func TestHTTPReaderFullResponse(t *testing.T) {
	body := []byte("0123456789")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body) // ignores Range => 200 OK with full body
	}))
	defer srv.Close()
	r := newHTTPReader(srv.URL, HTTPOptions{}.toInternal())
	got, err := r.readAt(2, 3)
	if err != nil || string(got) != "234" {
		t.Fatalf("full-response slice = %q, %v", got, err)
	}
	// Request beyond the body length => ErrUnexpectedEOF.
	if _, err := r.readAt(8, 5); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("full-response overrun = %v", err)
	}
}

func TestHTTPReaderErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	r := newHTTPReader(srv.URL, HTTPOptions{}.toInternal())
	if _, err := r.readAt(0, 4); err == nil {
		t.Fatal("expected error status")
	}
}

func TestHTTPReaderRetry(t *testing.T) {
	body := []byte("retryable-body-content")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	r := newHTTPReader(srv.URL, HTTPOptions{Retries: 2, RetryDelay: time.Millisecond}.toInternal())
	got, err := r.readAt(0, 5)
	if err != nil || string(got) != "retry" {
		t.Fatalf("retry = %q, %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

func TestHTTPReaderRequestError(t *testing.T) {
	// Unparseable URL makes http.NewRequest fail inside rangeRequest.
	r := newHTTPReader("http://%zz", HTTPOptions{}.toInternal())
	if _, err := r.readAt(0, 4); err == nil {
		t.Fatal("expected request build error")
	}
}

func TestHTTPReaderFullResponseBodyError(t *testing.T) {
	// 200 OK that promises more bytes than it delivers, so io.ReadAll fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Returning now closes the connection mid-body.
	}))
	defer srv.Close()
	r := newHTTPReader(srv.URL, HTTPOptions{}.toInternal())
	if _, err := r.readAt(0, 4); err == nil {
		t.Fatal("expected body read error")
	}
}

func TestHTTPReaderConnectionError(t *testing.T) {
	// Port 1 on loopback refuses connections, exercising the client.Do error
	// path. retries=0 so it returns after a single failed attempt.
	r := newHTTPReader("http://127.0.0.1:1/archive", HTTPOptions{Timeout: time.Second}.toInternal())
	if _, err := r.readAt(0, 4); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestHTTPReaderShortPartial(t *testing.T) {
	// 206 but the body is shorter than the requested length.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-99/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	r := newHTTPReader(srv.URL, HTTPOptions{}.toInternal())
	if _, err := r.readAt(0, 50); err == nil {
		t.Fatal("expected short partial error")
	}
}

func TestHTTPReaderCustomHeader(t *testing.T) {
	var gotHeader string
	body := []byte("authorized-content-here")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotHeader = req.Header.Get("X-Token")
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	h := http.Header{}
	h.Set("X-Token", "secret")
	r := newHTTPReader(srv.URL, HTTPOptions{Header: h}.toInternal())
	if _, err := r.readAt(0, 4); err != nil {
		t.Fatalf("custom header read: %v", err)
	}
	if gotHeader != "secret" {
		t.Fatalf("custom header not sent, got %q", gotHeader)
	}
}

func TestOpenArchiveHTTPAndClone(t *testing.T) {
	src := bytes.Repeat([]byte("integration test payload "), 4096)
	var arc bytes.Buffer
	if err := Compress(bytes.NewReader(src), &arc, CompressConfig{}); err != nil {
		t.Fatalf("compress: %v", err)
	}
	archiveBytes := arc.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.ServeContent(w, req, "archive.cba", time.Time{}, bytes.NewReader(archiveBytes))
	}))
	defer srv.Close()

	a, err := OpenArchiveHTTP(srv.URL, HTTPOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open http: %v", err)
	}
	out := newMemTarget()
	if _, err := Clone(a, out, CloneOptions{VerifyOutput: true}); err != nil {
		t.Fatalf("clone http: %v", err)
	}
	if !bytes.Equal(out.bytes(), src) {
		t.Fatal("http clone output mismatch")
	}
}
