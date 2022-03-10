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
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
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

	hashes = map[string]func() hash.Hash{
		"md5":    md5.New,
		"sha1":   sha1.New,
		"sha256": sha256.New,
		"sha512": sha512.New,
	}

	flagAddress = flag.String("a", defaultAddr, "server address:port")
	flagBufSize = flag.Int("b", defaultBufSize, "buffer size in bytes")
	flagKeep    = flag.Bool("k", false, "don't remove stored stdin file")
	flagLog     = flag.Bool("log", false, "enable verbose logging")
	flagTempDir = flag.String("tempdir", os.TempDir(), "temporary directory for storing stdin in a file")
)

type server struct {
	http.Server
	file      offeredFile
	sumsCache sync.Map
}

type offeredFile struct {
	name   string
	isTemp bool
	buf    []byte
}

func main() {
	flag.Parse()

	// TODO check flags values before using them, add a checkFlags() func.
	// TODO Content-Disposition filename and custom filename with -f flag.
	// TODO Cache headers?
	// TODO disable file buffering with -b 0 ?
	// TODO add a timeout for server shutdown and a -t flag to change it?
	// TODO or a -n flag for allowing just n requests?
	// TODO basic authentication with -u flag?
	// TODO add stream mode for stdin? i.e. don't stash stdin in a tmp file,
	//      allow only one request and disable checksums. useful to avoid
	//      to write big files on disk if it's only needed once.
	// TODO -r flag for receiving a file? i.e. receive an offer eheh.
	// TODO handle range requests? maybe would be better to use http.ServeContent()

	if !*flagLog {
		log.SetOutput(io.Discard)
	}
	log.Printf("%s %s pid %d", progName, progVersion, os.Getpid())

	if err := run(); err != nil {
		log.Println(err)
		fmt.Fprintf(os.Stderr, "%s: %s\n", progName, err)
		os.Exit(1)
	}
}

func run() error {
	if *flagBufSize <= 0 {
		return fmt.Errorf("%d: %w", *flagBufSize, errInvalidBufSize)
	}

	var of offeredFile
	var err error
	switch {
	case len(flag.Args()) == 0:
		fallthrough
	case len(flag.Args()) == 1 && flag.Args()[0] == "-":
		if of, err = offerStdin(*flagBufSize, *flagTempDir); err != nil {
			return err
		}
	case len(flag.Args()) == 1:
		if of, err = offerFile(flag.Args()[0], *flagBufSize); err != nil {
			return err
		}
	default:
		return errTooManyFiles
	}

	srv := &server{file: of}
	srv.Addr = *flagAddress
	srv.Handler = srv.wrap(http.DefaultServeMux)
	http.HandleFunc("/", srv.offerFile)
	http.HandleFunc("/checksums", srv.allChecksums)
	http.Handle("/checksums/", http.StripPrefix("/checksums/", http.HandlerFunc(srv.singleChecksum)))

	waitConns := make(chan struct{})
	go func() {
		sigch := make(chan os.Signal, 1)
		signal.Notify(sigch, os.Interrupt, syscall.SIGTERM)
		sig := <-sigch
		log.Printf("got signal %q shutting down", sig)
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Println(err)
		}
		close(waitConns)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Println("waiting for active connections")
	<-waitConns

	if of.isTemp && !*flagKeep {
		log.Printf("removing %s", of.name)
		return os.Remove(of.name)
	}
	return nil
}

func offerStdin(bufSize int, tempDir string) (offeredFile, error) {
	buf, err := tryReadAll(os.Stdin, bufSize)
	if err == nil {
		name := fmt.Sprintf("%s-%d", progName, time.Now().Unix())
		return offeredFile{name: name, buf: buf}, nil
	}
	if !errors.Is(err, errTooBig) {
		return offeredFile{}, fmt.Errorf("tryReadAll: %w", err)
	}
	tmp, err := os.CreateTemp(tempDir, fmt.Sprintf("%s-*", progName))
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
	log.Printf("saved stdin to %s", tmp.Name())
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

func (srv *server) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL)

		w.Header().Add("Server", fmt.Sprintf("%s %s", progName, progVersion))

		h.ServeHTTP(w, r)
	})
}

func (srv *server) offerFile(w http.ResponseWriter, r *http.Request) {
	if n, err := srv.file.copyTo(w); err != nil {
		log.Println(err)
		if n == 0 {
			http.Error(w, http.StatusText(404), 404)
		}
	}
}

func (srv *server) singleChecksum(w http.ResponseWriter, r *http.Request) {
	algo := strings.ToLower(r.URL.Path)
	if algo == "" {
		// requests with trailing slash in (i.e. /checksums/) get routed
		// here, but that means all checksums.
		srv.allChecksums(w, r)
		return
	}
	if cachedSum, ok := srv.sumsCache.Load(algo); ok {
		io.Copy(w, strings.NewReader(cachedSum.(string)))
		return
	}
	sum, err := srv.file.checksum(algo)
	if err != nil {
		log.Println(err)
		status := 500
		if errors.Is(err, errUnknownAlgo) {
			status = 404
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	formattedSum := formatSum(sum, algo, srv.file.name)
	srv.sumsCache.Store(algo, formattedSum)
	io.Copy(w, strings.NewReader(formattedSum))
}

func formatSum(sum []byte, algo, fpath string) string {
	// TODO use same format of coreutils?
	return fmt.Sprintf("%s %x %s\n", algo, sum, path.Base(fpath))
}

func (srv *server) allChecksums(w http.ResponseWriter, r *http.Request) {
	if cachedSum, ok := srv.sumsCache.Load(""); ok {
		io.Copy(w, strings.NewReader(cachedSum.(string)))
		return
	}
	var algos []string
	for algo, _ := range hashes {
		algos = append(algos, algo)
	}
	sort.Strings(algos)
	var formattedSums strings.Builder
	for _, algo := range algos {
		if cachedSum, ok := srv.sumsCache.Load(algo); ok {
			formattedSums.WriteString(cachedSum.(string))
			continue
		}
		sum, err := srv.file.checksum(algo)
		if err != nil {
			log.Println(err)
			http.Error(w, http.StatusText(500), 500)
			return
		}
		formattedSum := formatSum(sum, algo, srv.file.name)
		srv.sumsCache.Store(algo, formattedSum)
		formattedSums.WriteString(formattedSum)
	}
	srv.sumsCache.Store("", formattedSums.String())
	io.Copy(w, strings.NewReader(formattedSums.String()))
}
