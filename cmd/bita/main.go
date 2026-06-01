// bita is a pure-Go, oll3/bita-interoperable implementation of bita's
// differential file synchronization:
//
//	bita compress [-i INPUT] OUTPUT.cba
//	bita clone [--seed FILE]... ARCHIVE OUTPUT
//	bita info ARCHIVE
//
// ARCHIVE may be a local path or an http(s) URL. A single dash ("-") denotes
// stdin for the compress input and for seeds.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-deltasync/bita/internal/bita"
	"github.com/spf13/cobra"
)

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "bita",
		Short:         "Pure-Go, oll3/bita-compatible differential file sync",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(compressCmd(), cloneCmd(), infoCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "bita: %v\n", err)
		os.Exit(1)
	}
}

func compressCmd() *cobra.Command {
	var (
		input       string
		conf        bita.CompressConfig
		metadataKVs []string
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "compress [flags] OUTPUT",
		Short: "Create a .cba archive from INPUT",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			md, err := parseMetadata(metadataKVs)
			if err != nil {
				return err
			}
			conf.Metadata = md

			in, closeIn, err := openInput(input)
			if err != nil {
				return err
			}
			defer closeIn()

			if !force {
				if _, err := os.Stat(args[0]); err == nil {
					return fmt.Errorf("%s already exists (use --force to overwrite)", args[0])
				}
			}
			out, err := os.Create(args[0])
			if err != nil {
				return err
			}
			defer func() { _ = out.Close() }()
			return bita.Compress(in, out, conf)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&input, "input", "i", "-", `input file ("-" for stdin)`)
	f.StringVar(&conf.Algorithm, "hash-chunking", bita.AlgoRollSum, "chunking algorithm: rollsum|buzhash|fixed")
	f.IntVar(&conf.AvgChunkSize, "avg-chunk-size", 64*1024, "average target chunk size (bytes)")
	f.IntVar(&conf.MinChunkSize, "min-chunk-size", 16*1024, "minimum chunk size (bytes)")
	f.IntVar(&conf.MaxChunkSize, "max-chunk-size", 16*1024*1024, "maximum chunk size (bytes)")
	f.IntVar(&conf.WindowSize, "rolling-window-size", 0, "rolling hash window size (default 64 rollsum / 16 buzhash)")
	f.IntVar(&conf.FixedSize, "fixed-size", 64*1024, "chunk size for fixed-size chunking")
	f.StringVar(&conf.Compression, "compression", bita.CompBrotli, "chunk compression: brotli|zstd|none")
	f.IntVar(&conf.CompressionLevel, "compression-level", 6, "compression level")
	f.IntVar(&conf.HashLength, "hash-length", 64, "chunk hash length (bytes, 1-64)")
	f.StringArrayVar(&metadataKVs, "metadata", nil, "metadata key=value (repeatable)")
	f.BoolVar(&force, "force", false, "overwrite OUTPUT if it exists")
	return cmd
}

func cloneCmd() *cobra.Command {
	var (
		seeds        []string
		verifyOutput bool
		verifyHeader string
		force        bool
		httpRetries  int
		httpDelay    time.Duration
		httpTimeout  time.Duration
		httpHeaders  []string
	)
	cmd := &cobra.Command{
		Use:   "clone [flags] ARCHIVE OUTPUT",
		Short: "Reconstruct a file from an archive, reusing seeds",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			hdr, err := parseHeaders(httpHeaders)
			if err != nil {
				return err
			}
			archive, err := openArchive(args[0], bita.HTTPOptions{
				Retries:    httpRetries,
				RetryDelay: httpDelay,
				Timeout:    httpTimeout,
				Header:     hdr,
			})
			if err != nil {
				return err
			}
			if verifyHeader != "" {
				if err := archive.VerifyHeader(verifyHeader); err != nil {
					return err
				}
			}

			seedReaders, closeSeeds, err := openSeeds(seeds)
			if err != nil {
				return err
			}
			defer closeSeeds()

			if !force {
				if _, err := os.Stat(args[1]); err == nil {
					return fmt.Errorf("%s already exists (use --force to overwrite)", args[1])
				}
			}
			out, err := os.OpenFile(args[1], os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			defer func() { _ = out.Close() }()

			stats, err := bita.Clone(archive, out, bita.CloneOptions{
				Seeds:        seedReaders,
				VerifyOutput: verifyOutput,
			})
			if err != nil {
				return err
			}
			if err := out.Truncate(int64(stats.TotalSize)); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "cloned %d bytes (%d from seeds, %d from archive)\n",
				stats.TotalSize, stats.FromSeed, stats.FromArchive)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&seeds, "seed", nil, `seed file ("-" for stdin, repeatable)`)
	f.BoolVar(&verifyOutput, "verify-output", false, "verify the reconstructed output checksum")
	f.StringVar(&verifyHeader, "verify-header", "", "expected archive header checksum (hex)")
	f.BoolVar(&force, "force", false, "overwrite OUTPUT if it exists")
	f.IntVar(&httpRetries, "http-retry-count", 0, "number of HTTP retries")
	f.DurationVar(&httpDelay, "http-retry-delay", 0, "delay between HTTP retries")
	f.DurationVar(&httpTimeout, "http-timeout", 0, "HTTP request timeout (0 = none)")
	f.StringArrayVar(&httpHeaders, "http-header", nil, "extra HTTP header key:value (repeatable)")
	return cmd
}

func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info ARCHIVE",
		Short: "Print archive metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			archive, err := openArchive(args[0], bita.HTTPOptions{})
			if err != nil {
				return err
			}
			i := archive.Info()
			fmt.Printf("Built with:        %s\n", i.BuiltWith)
			fmt.Printf("Source size:       %d bytes\n", i.SourceSize)
			fmt.Printf("Source checksum:   %s\n", i.SourceChecksum)
			fmt.Printf("Header checksum:   %s\n", i.HeaderChecksum)
			fmt.Printf("Chunking:          %s\n", i.Algorithm)
			if i.Algorithm != bita.AlgoFixed {
				fmt.Printf("Avg chunk size:    %d bytes\n", i.AvgChunkSize)
				fmt.Printf("Window size:       %d bytes\n", i.WindowSize)
			}
			fmt.Printf("Min/Max chunk:     %d / %d bytes\n", i.MinChunkSize, i.MaxChunkSize)
			fmt.Printf("Hash length:       %d bytes\n", i.HashLength)
			fmt.Printf("Compression:       %s (level %d)\n", i.Compression, i.CompressionLevel)
			fmt.Printf("Chunks:            %d (%d unique)\n", i.SourceChunks, i.UniqueChunks)
			for k, v := range i.Metadata {
				fmt.Printf("Metadata[%s]:      %q\n", k, string(v))
			}
			return nil
		},
	}
}

func openArchive(path string, httpOpts bita.HTTPOptions) (*bita.Archive, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return bita.OpenArchiveHTTP(path, httpOpts)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// The file handle is intentionally left open for the lifetime of the
	// process; it is reclaimed on exit.
	return bita.OpenArchiveReaderAt(f)
}

func openInput(path string) (io.Reader, func(), error) {
	if path == "-" || path == "" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

func openSeeds(paths []string) ([]io.Reader, func(), error) {
	var readers []io.Reader
	var closers []func()
	closeAll := func() {
		for _, c := range closers {
			c()
		}
	}
	for _, p := range paths {
		if p == "-" {
			readers = append(readers, os.Stdin)
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		readers = append(readers, f)
		closers = append(closers, func() { _ = f.Close() })
	}
	return readers, closeAll, nil
}

func parseMetadata(kvs []string) (map[string][]byte, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	md := make(map[string][]byte, len(kvs))
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("invalid metadata %q (want key=value)", kv)
		}
		md[k] = []byte(v)
	}
	return md, nil
}

func parseHeaders(hs []string) (http.Header, error) {
	if len(hs) == 0 {
		return nil, nil
	}
	hdr := make(http.Header, len(hs))
	for _, h := range hs {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			return nil, fmt.Errorf("invalid header %q (want key:value)", h)
		}
		hdr.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return hdr, nil
}
