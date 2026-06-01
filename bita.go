// Package bita is a pure-Go, cgo-free, oll3/bita-interoperable implementation of
// bita's differential file synchronization: chunk and compress a file into a
// .cba archive, then reconstruct ("clone") it elsewhere by reusing chunks that
// are already available locally (seeds) and fetching only the rest, from a local
// path or an HTTP server.
//
//	var arc bytes.Buffer
//	_ = bita.Compress(input, &arc, bita.CompressConfig{})
//	a, _ := bita.OpenArchiveReaderAt(bytes.NewReader(arc.Bytes()))
//	_, _ = bita.Clone(a, out, bita.CloneOptions{Seeds: []io.Reader{seed}})
package bita

import (
	"io"

	impl "github.com/go-deltasync/bita/internal/bita"
)

// Chunker algorithm names accepted by CompressConfig.Algorithm.
const (
	AlgoRollSum = impl.AlgoRollSum
	AlgoBuzHash = impl.AlgoBuzHash
	AlgoFixed   = impl.AlgoFixed
)

// Compression names accepted by CompressConfig.Compression.
const (
	CompBrotli = impl.CompBrotli
	CompZstd   = impl.CompZstd
	CompNone   = impl.CompNone
)

// CompressConfig holds the user-facing options for building an archive.
type CompressConfig = impl.CompressConfig

// HTTPOptions configures an HTTP-backed archive.
type HTTPOptions = impl.HTTPOptions

// Archive is an opened bita archive ready for cloning or inspection.
type Archive = impl.Archive

// ArchiveInfo summarises an archive (see Archive.Info).
type ArchiveInfo = impl.ArchiveInfo

// CloneTarget is the output of a clone: random-access write plus read-back for
// verification (e.g. *os.File).
type CloneTarget = impl.CloneTarget

// CloneOptions configures a clone operation.
type CloneOptions = impl.CloneOptions

// CloneStats reports how the output was assembled.
type CloneStats = impl.CloneStats

// Compress reads the input, chunks it, deduplicates, compresses unique chunks
// and writes a .cba archive to out.
func Compress(in io.Reader, out io.Writer, conf CompressConfig) error {
	return impl.Compress(in, out, conf)
}

// OpenArchiveReaderAt opens an archive backed by an io.ReaderAt (e.g. *os.File).
func OpenArchiveReaderAt(r io.ReaderAt) (*Archive, error) {
	return impl.OpenArchiveReaderAt(r)
}

// OpenArchiveHTTP opens an archive served over HTTP(S) using Range requests.
func OpenArchiveHTTP(url string, opts HTTPOptions) (*Archive, error) {
	return impl.OpenArchiveHTTP(url, opts)
}

// Clone reconstructs the source described by the archive into out, reusing
// chunks found in the provided seeds and fetching the rest from the archive.
func Clone(a *Archive, out CloneTarget, opts CloneOptions) (CloneStats, error) {
	return impl.Clone(a, out, opts)
}
