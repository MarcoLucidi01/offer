// See license file for copyright and license details.

package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	progName = "offer"
	progRepo = "https://github.com/MarcoLucidi01/offer"

	defaultAddr    = ":8080"
	defaultBufSize = 20 * (1 << 20) // MiB
)

var (
	progVersion = "vX.Y.Z-dev" // set with -ldflags at build time

	errInvalidBufSize = errors.New("invalid buffer size")
	errIsDir          = errors.New("is a directory")
	errTooBig         = errors.New("too big")
	errTooManyFiles   = errors.New("too many files")
	errUnknownAlgo    = errors.New("unknown hash algorithm")

	flagAddress = flag.String("a", defaultAddr, "server address:port")
	flagBufSize = flag.Int("b", defaultBufSize, "buffer size in bytes")

	hashes = map[string]func() hash.Hash{
		"md5":    md5.New,
		"sha1":   sha1.New,
		"sha256": sha256.New,
		"sha512": sha512.New,
	}
)

type server struct {
	http.Server
	file offeredFile
}

type offeredFile struct {
	name   string
	isTemp bool
	buf    []byte
}

func main() {
	flag.Parse()

	// TODO verbose logging.
	// TODO Content-Disposition filename and custom filename with -f flag.
	// TODO Server header.
	// TODO Cache headers?
	// TODO disable file buffering with -b 0 ?
	// TODO flag for changing tmp folder.
	// TODO flag for keeping tmp files.
	// TODO add a timeout for server shutdown and a -t flag to change it?
	// TODO or a -n flag for allowing just n requests?
	// TODO basic authentication with -u flag?
	// TODO add stream mode for stdin? i.e. don't stash stdin in a tmp file,
	//      allow only one request and disable checksums. useful to avoid
	//      to write big files on disk if it's only needed once.
	// TODO -r flag for receiving a file? i.e. receive an offer eheh.
	// TODO handle range requests? maybe would be better to use http.ServeContent()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", progName, err)
		os.Exit(1)
	}
}

func run() error {
	if *flagBufSize <= 0 {
		return errInvalidBufSize
	}

	var of offeredFile
	var err error
	switch {
	case len(flag.Args()) == 0:
		fallthrough
	case len(flag.Args()) == 1 && flag.Args()[0] == "-":
		if of, err = offerStdin(*flagBufSize); err != nil {
			return err
		}
	case len(flag.Args()) == 1:
		if of, err = offerFile(flag.Args()[0], *flagBufSize); err != nil {
			return err
		}
	default:
		return errTooManyFiles
	}

	if of.isTemp {
		defer os.Remove(of.name)
	}

	srv := &server{file: of}
	srv.Addr = *flagAddress
	http.Handle("/", srv.offerFile())
	http.Handle("/checksums/", http.StripPrefix("/checksums/", srv.checksums()))

	waitIdleConns := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint
		if err := srv.Shutdown(context.Background()); err != nil {
			// TODO log
		}
		close(waitIdleConns)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-waitIdleConns
	return nil
}

func offerStdin(bufSize int) (offeredFile, error) {
	buf, err := tryReadAll(os.Stdin, bufSize)
	if err == nil {
		name := fmt.Sprintf("%s-%d", progName, time.Now().Unix())
		return offeredFile{name: name, buf: buf}, nil
	}
	if !errors.Is(err, errTooBig) {
		return offeredFile{}, fmt.Errorf("tryReadAll: %w", err)
	}
	tmp, err := os.CreateTemp("", progName+"-*")
	if err != nil {
		return offeredFile{}, err
	}
	defer tmp.Close()
	_, err = io.Copy(tmp, bytes.NewReader(buf))
	if err == nil {
		_, err = io.Copy(tmp, os.Stdin)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return offeredFile{}, err
	}
	return offeredFile{name: tmp.Name(), isTemp: true}, nil
}

func offerFile(fname string, bufSize int) (offeredFile, error) {
	finfo, err := os.Stat(fname)
	if err != nil {
		return offeredFile{}, err
	}
	if finfo.IsDir() {
		return offeredFile{}, fmt.Errorf("%s: %w", fname, errIsDir)
	}
	if finfo.Size() > int64(bufSize) {
		return offeredFile{name: fname}, nil
	}
	f, err := os.Open(fname)
	if err != nil {
		return offeredFile{}, err
	}
	defer f.Close()
	buf, err := tryReadAll(f, bufSize)
	if err != nil {
		return offeredFile{}, err
	}
	return offeredFile{name: fname, buf: buf}, nil
}

func tryReadAll(r io.Reader, bufSize int) ([]byte, error) {
	buf := make([]byte, bufSize)
	n, err := io.ReadFull(r, buf)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return buf[:n], nil
		}
		return buf[:n], err
	}
	buf2 := make([]byte, 1)
	if n2, err := r.Read(buf2); n2 > 0 || !errors.Is(err, io.EOF) {
		if n2 > 0 {
			buf = append(buf, buf2...)
			n += n2
		}
		return buf[:n], errTooBig
	}
	return buf[:n], nil
}

func (of offeredFile) copyTo(w io.Writer) (int64, error) {
	if len(of.buf) > 0 {
		return io.Copy(w, bytes.NewReader(of.buf))
	}
	f, err := os.Open(of.name)
	if err != nil {
		return 0, err
	}
	return io.Copy(w, f)
}

func (of offeredFile) checksum(algo string) ([]byte, error) {
	fn, ok := hashes[algo]
	if !ok {
		return nil, fmt.Errorf("%s: %w", algo, errUnknownAlgo)
	}
	h := fn()
	if _, err := of.copyTo(h); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func (srv *server) offerFile() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		srv.file.copyTo(w)
	}
}

func (srv *server) checksums() http.HandlerFunc {
	var cache sync.Map
	return func(w http.ResponseWriter, r *http.Request) {
		algo := strings.ToLower(r.URL.Path)

		if cachedSum, ok := cache.Load(algo); ok {
			io.Copy(w, strings.NewReader(cachedSum.(string)))
			return
		}

		if algo != "" {
			sum, err := srv.file.checksum(algo)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
				return
			}
			fmtSum := fmt.Sprintf("%s %x %s\n", algo, sum, path.Base(srv.file.name))
			cache.Store(algo, fmtSum)
			io.Copy(w, strings.NewReader(fmtSum))
			return
		}

		var algos []string
		for a, _ := range hashes {
			algos = append(algos, a)
		}
		sort.Strings(algos)
		var fmtSums strings.Builder
		for _, a := range algos {
			if cachedSum, ok := cache.Load(a); ok {
				fmtSums.WriteString(cachedSum.(string))
				continue
			}
			sum, err := srv.file.checksum(a)
			if err != nil {
				http.Error(w, http.StatusText(500), 500)
				return
			}
			fmtSum := fmt.Sprintf("%s %x %s\n", a, sum, path.Base(srv.file.name))
			cache.Store(a, fmtSum)
			fmtSums.WriteString(fmtSum)
		}
		cache.Store(algo, fmtSums.String())
		io.Copy(w, strings.NewReader(fmtSums.String()))
	}
}
