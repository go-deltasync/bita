package bita

import (
	"bytes"
	"fmt"
	"io"

	"golang.org/x/crypto/blake2b"
)

// CloneTarget is the output of a clone: random-access write plus read-back for
// verification. *os.File satisfies this.
type CloneTarget interface {
	io.WriterAt
	io.ReaderAt
}

// CloneOptions configures a clone operation.
type CloneOptions struct {
	// Seeds are existing data sources (previous versions, stdin, ...) chunked
	// with the archive's chunker config to avoid fetching matching chunks.
	Seeds []io.Reader
	// VerifyOutput re-hashes the written output and compares it to the source
	// checksum stored in the archive.
	VerifyOutput bool
}

// CloneStats reports how the output was assembled.
type CloneStats struct {
	FromSeed    uint64
	FromArchive uint64
	TotalSize   uint64
}

type chunkLoc struct {
	size    int
	offsets []uint64
}

// sourceIndex maps each chunk hash to every source offset it occupies, walking
// rebuild_order exactly as bita's build_source_index does.
func (a *Archive) sourceIndex() map[string]*chunkLoc {
	idx := make(map[string]*chunkLoc)
	var off uint64
	for _, ui := range a.dict.rebuildOrder {
		cd := a.dict.chunkDescriptors[ui]
		key := string(cd.checksum)
		loc := idx[key]
		if loc == nil {
			loc = &chunkLoc{size: int(cd.sourceSize)}
			idx[key] = loc
		}
		loc.offsets = append(loc.offsets, off)
		off += uint64(cd.sourceSize)
	}
	return idx
}

func writeChunk(out io.WriterAt, offsets []uint64, data []byte) error {
	for _, off := range offsets {
		if _, err := out.WriteAt(data, int64(off)); err != nil {
			return fmt.Errorf("bita: write output: %w", err)
		}
	}
	return nil
}

// Clone reconstructs the source described by the archive into out, reusing
// chunks found in the provided seeds and fetching the rest from the archive.
func Clone(a *Archive, out CloneTarget, opts CloneOptions) (CloneStats, error) {
	idx := a.sourceIndex()
	stats := CloneStats{TotalSize: a.dict.sourceTotalSize}

	// Seed stage: chunk each seed with the archive's config and place matches.
	for _, seed := range opts.Seeds {
		ch := newChunker(seed, a.cfg)
		for {
			_, data, err := ch.next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return stats, err
			}
			full := blake2b.Sum512(data)
			key := string(full[:a.hashLength])
			loc, ok := idx[key]
			if !ok {
				continue
			}
			if err := writeChunk(out, loc.offsets, data); err != nil {
				return stats, err
			}
			stats.FromSeed += uint64(loc.size) * uint64(len(loc.offsets))
			delete(idx, key)
		}
	}

	// Fetch stage: pull the remaining chunks from the archive.
	if len(idx) > 0 {
		var ranges []chunkRange
		var need []chunkDescriptor
		for _, cd := range a.dict.chunkDescriptors {
			if _, ok := idx[string(cd.checksum)]; !ok {
				continue
			}
			ranges = append(ranges, chunkRange{
				offset: a.chunkDataOffset + cd.archiveOffset,
				size:   int(cd.archiveSize),
				index:  len(need),
			})
			need = append(need, cd)
		}
		blobs, err := readRanges(a.reader, ranges)
		if err != nil {
			return stats, err
		}
		for i, cd := range need {
			raw := blobs[i]
			data := raw
			if int(cd.sourceSize) != len(raw) {
				data, err = decompressChunk(raw, a.compression, int(cd.sourceSize))
				if err != nil {
					return stats, err
				}
			}
			full := blake2b.Sum512(data)
			if !bytes.Equal(full[:a.hashLength], cd.checksum) {
				return stats, fmt.Errorf("bita: chunk checksum mismatch")
			}
			loc := idx[string(cd.checksum)]
			if err := writeChunk(out, loc.offsets, data); err != nil {
				return stats, err
			}
			stats.FromArchive += uint64(loc.size) * uint64(len(loc.offsets))
		}
	}

	if opts.VerifyOutput {
		if err := a.verifyOutput(out); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// verifyOutput re-hashes the written output and compares it to the archive's
// stored source checksum.
func (a *Archive) verifyOutput(out io.ReaderAt) error {
	h, _ := blake2b.New512(nil)
	sr := io.NewSectionReader(out, 0, int64(a.dict.sourceTotalSize))
	if _, err := io.Copy(h, sr); err != nil {
		return fmt.Errorf("bita: read output for verification: %w", err)
	}
	if !bytes.Equal(h.Sum(nil), a.dict.sourceChecksum) {
		return fmt.Errorf("bita: output verification failed")
	}
	return nil
}
