package bita

import (
	"fmt"
	"sort"
)

// Chunking algorithm identifiers (ChunkerParameters.chunking_algorithm).
const (
	algoBuzHash   int32 = 0
	algoRollSum   int32 = 1
	algoFixedSize int32 = 2
)

// Compression identifiers (ChunkCompression.compression).
const (
	compNone   int32 = 0
	compLZMA   int32 = 1
	compZstd   int32 = 2
	compBrotli int32 = 3
)

// chunkerParameters mirrors bita's ChunkerParameters proto message.
type chunkerParameters struct {
	chunkFilterBits       uint32
	minChunkSize          uint32
	maxChunkSize          uint32
	rollingHashWindowSize uint32
	chunkHashLength       uint32
	chunkingAlgorithm     int32
}

// chunkCompression mirrors bita's ChunkCompression proto message.
type chunkCompression struct {
	compression      int32
	compressionLevel uint32
}

// chunkDescriptor mirrors bita's ChunkDescriptor proto message. Note that
// archiveOffset is stored relative to the archive's chunk-data offset.
type chunkDescriptor struct {
	checksum      []byte
	archiveSize   uint32
	archiveOffset uint64
	sourceSize    uint32
}

// chunkDictionary mirrors bita's ChunkDictionary proto message.
type chunkDictionary struct {
	applicationVersion string
	sourceChecksum     []byte
	sourceTotalSize    uint64
	chunkerParams      *chunkerParameters
	chunkCompression   *chunkCompression
	rebuildOrder       []uint32
	chunkDescriptors   []chunkDescriptor
	metadata           map[string][]byte
}

func (c *chunkerParameters) marshal() []byte {
	var b []byte
	b = appendVarintField(b, 1, uint64(c.chunkFilterBits))
	b = appendVarintField(b, 2, uint64(c.minChunkSize))
	b = appendVarintField(b, 3, uint64(c.maxChunkSize))
	b = appendVarintField(b, 4, uint64(c.rollingHashWindowSize))
	b = appendVarintField(b, 5, uint64(c.chunkHashLength))
	b = appendVarintField(b, 6, uint64(c.chunkingAlgorithm))
	return b
}

func (c *chunkCompression) marshal() []byte {
	var b []byte
	b = appendVarintField(b, 2, uint64(c.compression))
	b = appendVarintField(b, 3, uint64(c.compressionLevel))
	return b
}

func (d *chunkDescriptor) marshal() []byte {
	var b []byte
	b = appendBytesField(b, 1, d.checksum)
	b = appendVarintField(b, 3, uint64(d.archiveSize))
	b = appendVarintField(b, 4, d.archiveOffset)
	b = appendVarintField(b, 5, uint64(d.sourceSize))
	return b
}

// marshal serialises the dictionary in a deterministic, prost-compatible way.
func (d *chunkDictionary) marshal() []byte {
	var b []byte
	b = appendBytesField(b, 1, []byte(d.applicationVersion))
	b = appendBytesField(b, 2, d.sourceChecksum)
	b = appendVarintField(b, 3, d.sourceTotalSize)
	if d.chunkerParams != nil {
		b = appendMessageField(b, 4, d.chunkerParams.marshal())
	}
	if d.chunkCompression != nil {
		b = appendMessageField(b, 5, d.chunkCompression.marshal())
	}
	b = appendPackedUint32(b, 6, d.rebuildOrder)
	for i := range d.chunkDescriptors {
		b = appendMessageField(b, 7, d.chunkDescriptors[i].marshal())
	}
	// Maps are emitted with keys sorted to match Rust's BTreeMap ordering.
	if len(d.metadata) > 0 {
		keys := make([]string, 0, len(d.metadata))
		for k := range d.metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var entry []byte
			entry = appendBytesField(entry, 1, []byte(k))
			entry = appendBytesField(entry, 2, d.metadata[k])
			b = appendMessageField(b, 8, entry)
		}
	}
	return b
}

func unmarshalChunkerParameters(b []byte) (*chunkerParameters, error) {
	c := &chunkerParameters{}
	p := newProtoBuf(b)
	for !p.eof() {
		field, wt, err := p.readTag()
		if err != nil {
			return nil, err
		}
		val, _, err := p.readValue(wt)
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			c.chunkFilterBits = uint32(val)
		case 2:
			c.minChunkSize = uint32(val)
		case 3:
			c.maxChunkSize = uint32(val)
		case 4:
			c.rollingHashWindowSize = uint32(val)
		case 5:
			c.chunkHashLength = uint32(val)
		case 6:
			c.chunkingAlgorithm = int32(val)
		}
	}
	return c, nil
}

func unmarshalChunkCompression(b []byte) (*chunkCompression, error) {
	c := &chunkCompression{}
	p := newProtoBuf(b)
	for !p.eof() {
		field, wt, err := p.readTag()
		if err != nil {
			return nil, err
		}
		val, _, err := p.readValue(wt)
		if err != nil {
			return nil, err
		}
		switch field {
		case 2:
			c.compression = int32(val)
		case 3:
			c.compressionLevel = uint32(val)
		}
	}
	return c, nil
}

func unmarshalChunkDescriptor(b []byte) (chunkDescriptor, error) {
	var d chunkDescriptor
	p := newProtoBuf(b)
	for !p.eof() {
		field, wt, err := p.readTag()
		if err != nil {
			return d, err
		}
		val, raw, err := p.readValue(wt)
		if err != nil {
			return d, err
		}
		switch field {
		case 1:
			d.checksum = append([]byte(nil), raw...)
		case 3:
			d.archiveSize = uint32(val)
		case 4:
			d.archiveOffset = val
		case 5:
			d.sourceSize = uint32(val)
		}
	}
	return d, nil
}

func unmarshalMapEntry(b []byte) (string, []byte, error) {
	var key string
	var val []byte
	p := newProtoBuf(b)
	for !p.eof() {
		field, wt, err := p.readTag()
		if err != nil {
			return "", nil, err
		}
		_, raw, err := p.readValue(wt)
		if err != nil {
			return "", nil, err
		}
		switch field {
		case 1:
			key = string(raw)
		case 2:
			val = append([]byte(nil), raw...)
		}
	}
	return key, val, nil
}

// unmarshalDictionary parses a serialised ChunkDictionary.
func unmarshalDictionary(b []byte) (*chunkDictionary, error) {
	d := &chunkDictionary{}
	p := newProtoBuf(b)
	for !p.eof() {
		field, wt, err := p.readTag()
		if err != nil {
			return nil, err
		}
		val, raw, err := p.readValue(wt)
		if err != nil {
			return nil, err
		}
		switch field {
		case 1:
			d.applicationVersion = string(raw)
		case 2:
			d.sourceChecksum = append([]byte(nil), raw...)
		case 3:
			d.sourceTotalSize = val
		case 4:
			if d.chunkerParams, err = unmarshalChunkerParameters(raw); err != nil {
				return nil, err
			}
		case 5:
			if d.chunkCompression, err = unmarshalChunkCompression(raw); err != nil {
				return nil, err
			}
		case 6:
			// Repeated uint32 may be packed (wireLen) or a single varint.
			if wt == wireLen {
				if d.rebuildOrder, err = parsePackedUint32(raw, d.rebuildOrder); err != nil {
					return nil, err
				}
			} else {
				d.rebuildOrder = append(d.rebuildOrder, uint32(val))
			}
		case 7:
			cd, err := unmarshalChunkDescriptor(raw)
			if err != nil {
				return nil, err
			}
			d.chunkDescriptors = append(d.chunkDescriptors, cd)
		case 8:
			k, v, err := unmarshalMapEntry(raw)
			if err != nil {
				return nil, err
			}
			if d.metadata == nil {
				d.metadata = make(map[string][]byte)
			}
			d.metadata[k] = v
		}
	}
	return d, nil
}

// validate performs basic sanity checks on a parsed dictionary.
func (d *chunkDictionary) validate() error {
	if d.chunkerParams == nil {
		return fmt.Errorf("bita: archive is missing chunker parameters")
	}
	return nil
}
