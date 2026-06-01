# bita

[![ci](https://github.com/go-deltasync/bita/actions/workflows/ci.yml/badge.svg)](https://github.com/go-deltasync/bita/actions/workflows/ci.yml)
[![compat](https://github.com/go-deltasync/bita/actions/workflows/compat.yml/badge.svg)](https://github.com/go-deltasync/bita/actions/workflows/compat.yml)
[![coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)](https://github.com/go-deltasync/bita/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-deltasync/bita.svg)](https://pkg.go.dev/github.com/go-deltasync/bita)
[![Go version](https://img.shields.io/github/go-mod/go-version/go-deltasync/bita)](go.mod)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)

Pure-Go, cgo-free, [`oll3/bita`](https://github.com/oll3/bita)-compatible
differential file synchronization. `bita` chunks a file with a content-defined
rolling-hash chunker, stores the unique chunks in a compressed `.cba` archive,
and reconstructs ("clones") the file elsewhere by reusing whatever chunks are
already available locally (the *seeds*) and fetching only the missing ones —
over a plain HTTP server if the archive is remote.

This is a clean-room reimplementation of bita's wire format. Its archives are
read by the reference Rust `bita` CLI, and it reads archives produced by it —
verified bidirectionally for both the RollSum and BuzHash chunkers (see
[compatibility](#compatibility)).

Part of the [`go-deltasync`](https://github.com/go-deltasync) family of
delta-sync tools.

## Install

```sh
go install github.com/go-deltasync/bita/cmd/bita@latest
```

The binary is statically linked (`CGO_ENABLED=0`) and cross-platform.

## Usage

```
bita compress [-i INPUT] OUTPUT.cba    # build an archive from INPUT (or stdin)
bita clone [--seed FILE]... ARCHIVE OUTPUT
bita info ARCHIVE
```

`ARCHIVE` may be a local path or an `http(s)://` URL. A single dash (`-`) means
stdin for the compress input and for seeds.

### Compress

```sh
# Default: RollSum chunking, ~64 KiB chunks, brotli compression.
bita compress -i disk.img disk.cba

# BuzHash chunker, zstd compression.
bita compress -i disk.img disk.cba --hash-chunking buzhash --compression zstd
```

Key flags: `--hash-chunking rollsum|buzhash|fixed`, `--avg-chunk-size`,
`--min-chunk-size`, `--max-chunk-size`, `--rolling-window-size`, `--fixed-size`,
`--compression brotli|zstd|none`, `--compression-level`, `--hash-length`,
`--metadata key=value`.

### Clone

```sh
# Reconstruct v2 from a remote archive, reusing the local v1 as a seed.
bita clone --seed v1.img https://example.com/v2.cba v2.img --verify-output
```

Only the chunks that differ between the seed and the target are downloaded. With
a close seed this is a fraction of the full file:

```
$ bita clone --seed v1.img v2.cba v2.img
cloned 4194304 bytes (4142202 from seeds, 52102 from archive)
```

Flags: `--seed` (repeatable, `-` for stdin), `--verify-output`,
`--verify-header CHECKSUM`, `--force`, and HTTP options
`--http-retry-count`, `--http-retry-delay`, `--http-timeout`, `--http-header`.

## Archive format (`.cba`)

```
| offset | size | description                                          |
|--------|------|------------------------------------------------------|
|      0 |    6 | magic "BITA1\0"                                      |
|      6 |    8 | dictionary length (u64 LE)                          |
|     14 |    n | protobuf-encoded ChunkDictionary                    |
|   14+n |    8 | chunk-data offset, absolute from start (u64 LE)     |
|   22+n |   64 | Blake2b-512 checksum over bytes [0 .. 22+n)          |
|   ...  |      | concatenated (compressed) unique chunk data         |
```

The dictionary records the chunker parameters, the compression, the list of
unique chunk descriptors (Blake2b-512 checksum, archive offset/size, source
size) and the `rebuild_order` mapping each source position to a unique chunk.
A chunk is stored uncompressed when compression did not make it smaller.
Chunk and source checksums are Blake2b-512.

## Compatibility

The build tag `compat` runs cross-implementation tests against the reference
Rust `bita` CLI (skipped if `bita` is not on `PATH`):

```sh
cargo install bita
go test -tags=compat ./internal/bita/...
```

These verify, for both RollSum and BuzHash:

- a Go-built archive is cloned correctly by Rust `bita`;
- a Rust-built archive is cloned correctly by this tool;
- the chunkers agree byte-for-byte — cloning a Rust-built archive while seeding
  with the original source fetches **zero** bytes from the archive.

A comparative performance test (`TestPerfVsRustBita`, same build tag) runs both
tools as subprocesses on identical inputs and reports compress/clone throughput
and archive size side by side.

LZMA-compressed archives can be opened and inspected but not yet decompressed;
in-place seeding (`--seed-output`) is not yet implemented.

## Performance

Per-chunk work runs in parallel across CPU cores (mirroring bita's
`num_chunk_buffers` pipeline): chunk hashing and compression on `compress`,
chunk hashing of seeds and chunk decompression/verification on `clone`. The
inherently sequential rolling-hash scan, deduplication and archive layout stay
serial, so the output is byte-identical to a single-threaded run. Before the
(expensive) brotli pass, a fast zstd probe skips compression for chunks with no
exploitable redundancy — avoiding work that the "store uncompressed when not
smaller" rule would discard anyway.

### Protocol

`TestPerfVsRustBita` (under `-tags=compat`) builds the Go CLI and invokes it and
the reference Rust `bita` as **subprocesses on identical files**, so timings are
apples-to-apples (each pays process startup + file I/O). It measures:

- **source**: 16 MiB of incompressible xorshift bytes (worst case — brotli can
  find nothing to remove, so every byte is hashed and compression-attempted);
- **compress**: build a `.cba` from the source (default RollSum chunker, brotli
  level 6 — identical settings on both sides);
- **clone (seed)**: reconstruct the source from the archive using a near seed
  (the source with 64 scattered 32-byte edits), i.e. the differential-sync fast
  path;
- throughput is the best of 3 runs; archive size is exact.

Reproduce with `cargo install bita && go test -tags=compat -v -run Perf ./internal/bita/`.

### Results

Measured on an Apple M4 Max (16 cores), Go 1.26, Rust `bita` 0.14.0:

| impl        | compress   | clone (seed) | archive bytes |
|-------------|------------|--------------|---------------|
| go-bita     | 275 MB/s   | 369 MB/s     | 16,807,200    |
| rust bita   | 263 MB/s   | 375 MB/s     | 16,807,189    |

Archive size matches the reference to **11 bytes**, and throughput is at parity
— notable for a pure-Go, cgo-free build versus optimized Rust. On this
incompressible worst case the zstd probe lets us skip brotli where the reference
still runs (and discards) it; on compressible inputs both run brotli and the
parallel pipeline keeps the two close. Numbers are machine-dependent and
indicative; rerun the test on your hardware for a local comparison.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
