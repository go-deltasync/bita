package bita

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/blake2b"
)

// Archive file format constants (see bitar/src/header.rs).
//
//	| off |size| description                                          |
//	|-----|----|------------------------------------------------------|
//	|   0 |  6 | magic "BITA1\0"                                      |
//	|   6 |  8 | dictionary length (u64 LE)                          |
//	|  14 |  n | protobuf-encoded ChunkDictionary                    |
//	| 14+n|  8 | chunk-data offset, absolute from start (u64 LE)     |
//	|22+n | 64 | Blake2b-512 checksum over bytes [0 .. 22+n)          |
var (
	archiveMagic = []byte("BITA1\x00")
	// legacyMagic is the byte-swapped magic emitted by very old bita versions.
	legacyMagic = []byte("\x00BITA1")
)

const (
	magicLen      = 6
	preHeaderSize = magicLen + 8 // magic + dictionary length
	headerHashLen = 64           // Blake2b-512
)

// buildHeader assembles the .cba header for a serialised dictionary. When
// chunkDataOffset is nil it defaults to the position right after the checksum,
// matching bita's header::build.
func buildHeader(dict []byte, chunkDataOffset *uint64) []byte {
	h := make([]byte, 0, preHeaderSize+len(dict)+8+headerHashLen)
	h = append(h, archiveMagic...)
	h = binary.LittleEndian.AppendUint64(h, uint64(len(dict)))
	h = append(h, dict...)
	var off uint64
	if chunkDataOffset != nil {
		off = *chunkDataOffset
	} else {
		off = uint64(len(h)) + 8 + headerHashLen
	}
	h = binary.LittleEndian.AppendUint64(h, off)
	sum := blake2b.Sum512(h)
	h = append(h, sum[:]...)
	return h
}

// parsedHeader holds the decoded contents of a .cba header.
type parsedHeader struct {
	dictionary      *chunkDictionary
	chunkDataOffset uint64
	checksum        []byte
}

// parseHeader reads and validates a .cba header through the archive reader.
func parseHeader(r archiveReader) (*parsedHeader, error) {
	pre, err := r.readAt(0, preHeaderSize)
	if err != nil {
		return nil, fmt.Errorf("bita: read pre-header: %w", err)
	}
	magic := pre[:magicLen]
	if !bytes.Equal(magic, archiveMagic) && !bytes.Equal(magic, legacyMagic) {
		return nil, fmt.Errorf("bita: not a bita archive (bad magic)")
	}
	dictLen := binary.LittleEndian.Uint64(pre[magicLen:])
	// Read dictionary + chunk-data offset + checksum in one shot.
	rest, err := r.readAt(uint64(preHeaderSize), int(dictLen)+8+headerHashLen)
	if err != nil {
		return nil, fmt.Errorf("bita: read header body: %w", err)
	}
	dictBuf := rest[:dictLen]
	offBuf := rest[dictLen : dictLen+8]
	gotSum := rest[dictLen+8:]

	// Recompute the Blake2b-512 over everything up to (but excluding) the sum.
	hasher, _ := blake2b.New512(nil)
	_, _ = hasher.Write(pre)
	_, _ = hasher.Write(dictBuf)
	_, _ = hasher.Write(offBuf)
	wantSum := hasher.Sum(nil)
	if !bytes.Equal(gotSum, wantSum) {
		return nil, fmt.Errorf("bita: header checksum mismatch")
	}

	dict, err := unmarshalDictionary(dictBuf)
	if err != nil {
		return nil, fmt.Errorf("bita: decode dictionary: %w", err)
	}
	if err := dict.validate(); err != nil {
		return nil, err
	}
	return &parsedHeader{
		dictionary:      dict,
		chunkDataOffset: binary.LittleEndian.Uint64(offBuf),
		checksum:        append([]byte(nil), gotSum...),
	}, nil
}
