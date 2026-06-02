package bita

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/blake2b"
)

// appVersion is stored in the archive's application_version field. It is purely
// informational; bita does not parse it.
const appVersion = "go-deltasync/bita"

// Chunker algorithm names accepted by CompressConfig.
const (
	AlgoRollSum = "rollsum"
	AlgoBuzHash = "buzhash"
	AlgoFixed   = "fixed"
)

// Compression names accepted by CompressConfig.
const (
	CompBrotli = "brotli"
	CompZstd   = "zstd"
	CompNone   = "none"
)

// Default chunker/compression parameters (matching bita's CLI defaults).
const (
	defaultAvgChunkSize = 64 * 1024
	defaultMinChunkSize = 16 * 1024
	defaultMaxChunkSize = 16 * 1024 * 1024
	defaultRollWindow   = 64
	defaultBuzWindow    = 16
	defaultBrotliLevel  = 6
	defaultHashLength   = 64
)

// CompressConfig holds the user-facing options for building an archive. Zero
// values are replaced by bita's defaults during Compress.
type CompressConfig struct {
	Algorithm        string // rollsum | buzhash | fixed (default rollsum)
	AvgChunkSize     int
	MinChunkSize     int
	MaxChunkSize     int
	WindowSize       int
	FixedSize        int
	Compression      string // brotli | zstd | none (default brotli)
	CompressionLevel int
	HashLength       int
	Metadata         map[string][]byte
}

func (c CompressConfig) resolve() (chunkerConfig, Compression, int, error) {
	hashLen := c.HashLength
	if hashLen == 0 {
		hashLen = defaultHashLength
	}
	if hashLen < 1 || hashLen > 64 {
		return chunkerConfig{}, Compression{}, 0, fmt.Errorf("bita: hash length %d out of range [1,64]", hashLen)
	}

	var algo int32
	switch c.Compression {
	case "", CompBrotli:
		algo = compBrotli
	case CompZstd:
		algo = compZstd
	case CompNone:
		algo = compNone
	default:
		return chunkerConfig{}, Compression{}, 0, fmt.Errorf("bita: unknown compression %q", c.Compression)
	}
	level := c.CompressionLevel
	if level == 0 {
		level = defaultBrotliLevel
	}
	comp := Compression{algorithm: algo, level: uint32(level)}

	var cfg chunkerConfig
	switch c.Algorithm {
	case AlgoFixed:
		size := c.FixedSize
		if size == 0 {
			size = defaultAvgChunkSize
		}
		cfg = chunkerConfig{algorithm: algoFixedSize, fixedSize: size}
	case "", AlgoRollSum, AlgoBuzHash:
		minSize := c.MinChunkSize
		if minSize == 0 {
			minSize = defaultMinChunkSize
		}
		maxSize := c.MaxChunkSize
		if maxSize == 0 {
			maxSize = defaultMaxChunkSize
		}
		avg := c.AvgChunkSize
		if avg == 0 {
			avg = defaultAvgChunkSize
		}
		win := c.WindowSize
		a := algoRollSum
		if c.Algorithm == AlgoBuzHash {
			a = algoBuzHash
			if win == 0 {
				win = defaultBuzWindow
			}
		} else if win == 0 {
			win = defaultRollWindow
		}
		cfg = chunkerConfig{
			algorithm:    a,
			filterBits:   filterBitsFromSize(uint32(avg)),
			minChunkSize: minSize,
			maxChunkSize: maxSize,
			windowSize:   win,
		}
	default:
		return chunkerConfig{}, Compression{}, 0, fmt.Errorf("bita: unknown chunking algorithm %q", c.Algorithm)
	}
	return cfg, comp, hashLen, nil
}

// paramsFromConfig builds the dictionary's ChunkerParameters from a config.
func paramsFromConfig(cfg chunkerConfig, hashLen int) *chunkerParameters {
	if cfg.algorithm == algoFixedSize {
		return &chunkerParameters{
			maxChunkSize:      uint32(cfg.fixedSize),
			chunkHashLength:   uint32(hashLen),
			chunkingAlgorithm: algoFixedSize,
		}
	}
	return &chunkerParameters{
		chunkFilterBits:       cfg.filterBits,
		minChunkSize:          uint32(cfg.minChunkSize),
		maxChunkSize:          uint32(cfg.maxChunkSize),
		rollingHashWindowSize: uint32(cfg.windowSize),
		chunkHashLength:       uint32(hashLen),
		chunkingAlgorithm:     cfg.algorithm,
	}
}

// parallelMap runs fn(0..n-1) across the given number of workers. Each index is
// processed exactly once; fn must only touch state private to its index (e.g.
// distinct slice elements), so no synchronization is needed in the callback.
func parallelMap(n, workers int, fn func(i int)) {
	if n == 0 {
		return
	}
	if workers > n {
		workers = n
	}
	if workers < 2 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var wg sync.WaitGroup
	var next int64 = -1
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(atomic.AddInt64(&next, 1))
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// Compress reads the input, chunks it, deduplicates, compresses unique chunks
// and writes a .cba archive to out. Per-chunk hashing and compression run in
// parallel across CPU cores (mirroring bita's num_chunk_buffers pipeline);
// chunking, deduplication and archive layout stay sequential so the output is
// deterministic and byte-identical to the single-threaded result.
func Compress(in io.Reader, out io.Writer, conf CompressConfig) error {
	cfg, comp, hashLen, err := conf.resolve()
	if err != nil {
		return err
	}

	// Stage 1 (sequential chunking + concurrent file-wide hash): chunk the
	// stream and buffer the chunks. The file-wide source checksum is a single
	// sequential Blake2b-512 (its digest cannot be split across cores without
	// changing it and breaking compat), but it is independent of the rolling
	// chunk scan, so it runs on its own goroutine fed in source order — Stage 1
	// then costs max(chunking, hashing) instead of their sum. Each chunk slice
	// is a fresh copy (chunker.emit), so sharing it with the hasher is race-free.
	srcHasher, _ := blake2b.New512(nil)
	hashCh := make(chan []byte, 64)
	hashDone := make(chan struct{})
	go func() {
		for data := range hashCh {
			_, _ = srcHasher.Write(data)
		}
		close(hashDone)
	}()
	var sourceLength uint64
	var chunks [][]byte
	ch := newChunker(in, cfg)
	for {
		_, data, err := ch.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			close(hashCh)
			<-hashDone
			return err
		}
		sourceLength += uint64(len(data))
		chunks = append(chunks, data)
		hashCh <- data
	}
	close(hashCh)
	<-hashDone

	// Stage 2 (parallel): hash every chunk.
	hashes := make([][64]byte, len(chunks))
	workers := runtime.NumCPU()
	parallelMap(len(chunks), workers, func(i int) {
		hashes[i] = blake2b.Sum512(chunks[i])
	})

	// Stage 3 (sequential): deduplicate by full hash in first-occurrence order.
	uniqueIndex := make(map[string]int)
	rebuildOrder := make([]uint32, len(chunks))
	var unique []int // indices into chunks, in descriptor order
	for i := range chunks {
		key := string(hashes[i][:])
		if idx, ok := uniqueIndex[key]; ok {
			rebuildOrder[i] = uint32(idx)
			continue
		}
		idx := len(unique)
		uniqueIndex[key] = idx
		unique = append(unique, i)
		rebuildOrder[i] = uint32(idx)
	}

	// Stage 4 (parallel): compress the unique chunks. A fast incompressibility
	// probe skips the expensive brotli pass for chunks that won't shrink.
	useData := make([][]byte, len(unique))
	parallelMap(len(unique), workers, func(j int) {
		data := chunks[unique[j]]
		if comp.skipCompression(data) {
			useData[j] = data
			return
		}
		compressed := comp.compress(data)
		if len(compressed) >= len(data) {
			useData[j] = data
		} else {
			useData[j] = compressed
		}
	})

	// Stage 5 (sequential): lay out descriptors and chunk data in order.
	descriptors := make([]chunkDescriptor, len(unique))
	var chunkData bytes.Buffer
	var archiveOffset uint64
	for j, ci := range unique {
		data := chunks[ci]
		use := useData[j]
		chunkData.Write(use)
		descriptors[j] = chunkDescriptor{
			checksum:      append([]byte(nil), hashes[ci][:hashLen]...),
			archiveSize:   uint32(len(use)),
			archiveOffset: archiveOffset,
			sourceSize:    uint32(len(data)),
		}
		archiveOffset += uint64(len(use))
	}

	dict := &chunkDictionary{
		applicationVersion: appVersion,
		sourceChecksum:     srcHasher.Sum(nil),
		sourceTotalSize:    sourceLength,
		chunkerParams:      paramsFromConfig(cfg, hashLen),
		chunkCompression:   comp.toDict(),
		rebuildOrder:       rebuildOrder,
		chunkDescriptors:   descriptors,
		metadata:           conf.Metadata,
	}
	header := buildHeader(dict.marshal(), nil)
	if _, err := out.Write(header); err != nil {
		return fmt.Errorf("bita: write header: %w", err)
	}
	if _, err := out.Write(chunkData.Bytes()); err != nil {
		return fmt.Errorf("bita: write chunk data: %w", err)
	}
	return nil
}

// Archive is an opened bita archive ready for cloning or inspection.
type Archive struct {
	reader          archiveReader
	dict            *chunkDictionary
	chunkDataOffset uint64
	headerChecksum  []byte
	cfg             chunkerConfig
	hashLength      int
	compression     int32
}

// OpenArchiveReaderAt opens an archive backed by an io.ReaderAt (e.g. *os.File).
func OpenArchiveReaderAt(r io.ReaderAt) (*Archive, error) {
	return openArchive(newIOReader(r))
}

// OpenArchiveHTTP opens an archive served over HTTP(S) using Range requests.
func OpenArchiveHTTP(url string, opts HTTPOptions) (*Archive, error) {
	return openArchive(newHTTPReader(url, opts.toInternal()))
}

func openArchive(r archiveReader) (*Archive, error) {
	h, err := parseHeader(r)
	if err != nil {
		return nil, err
	}
	d := h.dictionary
	cfg, err := configFromParams(d.chunkerParams)
	if err != nil {
		return nil, err
	}
	hashLen := int(d.chunkerParams.chunkHashLength)
	if hashLen < 1 || hashLen > 64 {
		return nil, fmt.Errorf("bita: invalid chunk hash length %d", hashLen)
	}
	var comp int32
	if d.chunkCompression != nil {
		comp = d.chunkCompression.compression
	}
	if comp < compNone || comp > compBrotli {
		return nil, fmt.Errorf("bita: unknown compression %d", comp)
	}
	// Validate rebuild_order indices against the descriptor table.
	for _, idx := range d.rebuildOrder {
		if int(idx) >= len(d.chunkDescriptors) {
			return nil, fmt.Errorf("bita: rebuild_order index %d out of range", idx)
		}
	}
	return &Archive{
		reader:          r,
		dict:            d,
		chunkDataOffset: h.chunkDataOffset,
		headerChecksum:  h.checksum,
		cfg:             cfg,
		hashLength:      hashLen,
		compression:     comp,
	}, nil
}

func configFromParams(p *chunkerParameters) (chunkerConfig, error) {
	switch p.chunkingAlgorithm {
	case algoFixedSize:
		return chunkerConfig{algorithm: algoFixedSize, fixedSize: int(p.maxChunkSize)}, nil
	case algoRollSum, algoBuzHash:
		return chunkerConfig{
			algorithm:    p.chunkingAlgorithm,
			filterBits:   p.chunkFilterBits,
			minChunkSize: int(p.minChunkSize),
			maxChunkSize: int(p.maxChunkSize),
			windowSize:   int(p.rollingHashWindowSize),
		}, nil
	default:
		return chunkerConfig{}, fmt.Errorf("bita: unknown chunking algorithm %d", p.chunkingAlgorithm)
	}
}

// HeaderChecksumHex returns the archive header's Blake2b-512 checksum in hex.
func (a *Archive) HeaderChecksumHex() string {
	return hex.EncodeToString(a.headerChecksum)
}

// VerifyHeader checks the archive header checksum against an expected hex value.
func (a *Archive) VerifyHeader(expectHex string) error {
	want, err := hex.DecodeString(expectHex)
	if err != nil {
		return fmt.Errorf("bita: invalid header checksum value: %w", err)
	}
	if !bytes.Equal(want, a.headerChecksum) {
		return fmt.Errorf("bita: header checksum mismatch")
	}
	return nil
}

// ArchiveInfo summarises an archive for the `info` command.
type ArchiveInfo struct {
	BuiltWith        string
	SourceSize       uint64
	SourceChecksum   string
	HeaderChecksum   string
	Algorithm        string
	AvgChunkSize     uint32
	MinChunkSize     uint32
	MaxChunkSize     uint32
	WindowSize       uint32
	HashLength       int
	Compression      string
	CompressionLevel uint32
	SourceChunks     int
	UniqueChunks     int
	Metadata         map[string][]byte
}

// Info returns a summary of the archive contents.
func (a *Archive) Info() ArchiveInfo {
	p := a.dict.chunkerParams
	info := ArchiveInfo{
		BuiltWith:      a.dict.applicationVersion,
		SourceSize:     a.dict.sourceTotalSize,
		SourceChecksum: hex.EncodeToString(a.dict.sourceChecksum),
		HeaderChecksum: a.HeaderChecksumHex(),
		Algorithm:      algorithmName(p.chunkingAlgorithm),
		MinChunkSize:   p.minChunkSize,
		MaxChunkSize:   p.maxChunkSize,
		WindowSize:     p.rollingHashWindowSize,
		HashLength:     a.hashLength,
		Compression:    compressionName(a.compression),
		SourceChunks:   len(a.dict.rebuildOrder),
		UniqueChunks:   len(a.dict.chunkDescriptors),
		Metadata:       a.dict.metadata,
	}
	if p.chunkingAlgorithm != algoFixedSize {
		info.AvgChunkSize = 1 << (p.chunkFilterBits + 1)
	}
	if a.compression != compNone {
		info.CompressionLevel = a.dict.chunkCompression.compressionLevel
	}
	return info
}

func algorithmName(a int32) string {
	switch a {
	case algoRollSum:
		return AlgoRollSum
	case algoBuzHash:
		return AlgoBuzHash
	default:
		return AlgoFixed
	}
}

func compressionName(c int32) string {
	switch c {
	case compBrotli:
		return CompBrotli
	case compZstd:
		return CompZstd
	case compLZMA:
		return "lzma"
	default:
		return CompNone
	}
}
