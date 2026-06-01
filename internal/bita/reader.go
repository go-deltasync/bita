package bita

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// archiveReader abstracts random-access reads of an archive, so the same clone
// logic works for a local file and an HTTP-hosted archive.
type archiveReader interface {
	// readAt returns exactly size bytes starting at offset.
	readAt(offset uint64, size int) ([]byte, error)
}

// ioReader reads an archive from any io.ReaderAt (e.g. *os.File, *bytes.Reader).
type ioReader struct {
	r io.ReaderAt
}

func newIOReader(r io.ReaderAt) *ioReader { return &ioReader{r: r} }

func (r *ioReader) readAt(offset uint64, size int) ([]byte, error) {
	buf := make([]byte, size)
	n, err := r.r.ReadAt(buf, int64(offset))
	if n < size {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("bita: short read at offset %d: wanted %d, got %d: %w", offset, size, n, err)
	}
	return buf, nil
}

// httpReader reads an archive over HTTP(S) using Range requests.
type httpReader struct {
	client     *http.Client
	url        string
	header     http.Header
	retries    int
	retryDelay time.Duration
}

// httpOptions configures an httpReader.
type httpOptions struct {
	retries    int
	retryDelay time.Duration
	timeout    time.Duration
	header     http.Header
}

// HTTPOptions are the user-facing options for an HTTP-backed archive.
type HTTPOptions struct {
	Retries    int
	RetryDelay time.Duration
	Timeout    time.Duration
	Header     http.Header
}

func (o HTTPOptions) toInternal() httpOptions {
	return httpOptions{
		retries:    o.Retries,
		retryDelay: o.RetryDelay,
		timeout:    o.Timeout,
		header:     o.Header,
	}
}

func newHTTPReader(url string, opts httpOptions) *httpReader {
	return &httpReader{
		client:     &http.Client{Timeout: opts.timeout},
		url:        url,
		header:     opts.header,
		retries:    opts.retries,
		retryDelay: opts.retryDelay,
	}
}

func (h *httpReader) readAt(offset uint64, size int) ([]byte, error) {
	if size == 0 {
		return []byte{}, nil
	}
	var lastErr error
	for attempt := 0; attempt <= h.retries; attempt++ {
		if attempt > 0 {
			time.Sleep(h.retryDelay)
		}
		data, err := h.rangeRequest(offset, size)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (h *httpReader) rangeRequest(offset uint64, size int) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, h.url, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range h.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	end := offset + uint64(size) - 1
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		buf := make([]byte, size)
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			return nil, fmt.Errorf("bita: short range response: %w", err)
		}
		return buf, nil
	case http.StatusOK:
		// Server ignored the Range header and returned the whole body; slice
		// out the region we asked for.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if uint64(len(body)) < offset+uint64(size) {
			return nil, io.ErrUnexpectedEOF
		}
		return body[offset : offset+uint64(size)], nil
	default:
		return nil, fmt.Errorf("bita: http range request failed: %s", resp.Status)
	}
}

// chunkRange identifies a contiguous region of the archive to read.
type chunkRange struct {
	offset uint64
	size   int
	// index ties the range back to its position in the caller's request so
	// results can be returned in the original order.
	index int
}

// readRanges fetches each requested range, coalescing adjacent ranges into a
// single underlying read (mirroring bita's HTTP reader, which merges adjacent
// chunks into one Range request). Results are returned in the input order.
func readRanges(r archiveReader, ranges []chunkRange) ([][]byte, error) {
	out := make([][]byte, len(ranges))
	if len(ranges) == 0 {
		return out, nil
	}
	// Work on a copy sorted by offset so we can detect adjacency.
	sorted := append([]chunkRange(nil), ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].offset < sorted[j].offset })

	i := 0
	for i < len(sorted) {
		start := sorted[i].offset
		end := start + uint64(sorted[i].size)
		j := i + 1
		for j < len(sorted) && sorted[j].offset == end {
			end += uint64(sorted[j].size)
			j++
		}
		blob, err := r.readAt(start, int(end-start))
		if err != nil {
			return nil, err
		}
		pos := 0
		for k := i; k < j; k++ {
			out[sorted[k].index] = blob[pos : pos+sorted[k].size]
			pos += sorted[k].size
		}
		i = j
	}
	return out, nil
}
