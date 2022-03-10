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
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	progName = "offer"
	progRepo = "https://github.com/MarcoLucidi01/offer"

	defaultAddr    = ":8080"
	defaultBufSize = 50 * (1 << 20) // MiB
)

var (
	progVersion = "vX.Y.Z-dev" // set with -ldflags at build time

	errInvalidBufSize = errors.New("invalid buffer size")
	errIsDir          = errors.New("is a directory")
	errTooBig         = errors.New("too big")
	errTooManyFiles   = errors.New("too many files")
	errEmptySum       = errors.New("empty checksum")

	flagAddress = flag.String("a", defaultAddr, "server address:port")
	flagBufSize = flag.Int("b", defaultBufSize, "buffer size in bytes")
	flagKeep    = flag.Bool("k", false, "don't remove stored stdin file")
	flagLog     = flag.Bool("log", false, "enable verbose logging")
	flagTempDir = flag.String("tempdir", os.TempDir(), "temporary directory for storing stdin in a file")
)

type file struct {
	name       string
	buf        []byte
	isBuffered bool
	isTemp     bool
}

func main() {
	flag.Parse()

	// TODO check flags values before using them, add a checkFlags() func.
	// TODO Content-Disposition filename and custom filename with -f flag.
	// TODO Cache headers?
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
	if *flagBufSize < 0 {
		return fmt.Errorf("%d: %w", *flagBufSize, errInvalidBufSize)
	}

	var f file
	var err error
	switch {
	case len(flag.Args()) == 0 || (len(flag.Args()) == 1 && flag.Args()[0] == "-"):
		if f, err = offerStdin(*flagBufSize, *flagTempDir); err != nil {
			return err
		}
	case len(flag.Args()) == 1:
		if f, err = offerFile(flag.Args()[0], *flagBufSize); err != nil {
			return err
		}
	default:
		return errTooManyFiles
	}

	srv := http.Server{
		Addr:    *flagAddress,
		Handler: wrap(http.DefaultServeMux),
	}
	http.HandleFunc("/", sendFile(f))
	http.HandleFunc("/checksums", sendError(404))
	http.HandleFunc("/checksums/", sendError(404))
	http.HandleFunc("/checksums/md5", sendChecksum(f, md5.New))
	http.HandleFunc("/checksums/sha1", sendChecksum(f, sha1.New))
	http.HandleFunc("/checksums/sha256", sendChecksum(f, sha256.New))
	http.HandleFunc("/checksums/sha512", sendChecksum(f, sha512.New))

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

	if f.isTemp && !*flagKeep {
		log.Printf("removing %s", f.name)
		return os.Remove(f.name)
	}
	return nil
}

func offerStdin(bufSize int, tempDir string) (file, error) {
	buf, err := tryReadAll(os.Stdin, bufSize)
	if err == nil {
		name := fmt.Sprintf("%s-%d", progName, time.Now().Unix())
		return file{name: name, buf: buf, isBuffered: true}, nil
	}
	if !errors.Is(err, errTooBig) {
		return file{}, err
	}
	tmp, err := os.CreateTemp(tempDir, fmt.Sprintf("%s-*", progName))
	if err != nil {
		return file{}, err
	}
	defer tmp.Close()
	_, err = io.Copy(tmp, bytes.NewReader(buf))
	if err == nil {
		_, err = io.Copy(tmp, os.Stdin)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return file{}, err
	}
	log.Printf("saved stdin to %s", tmp.Name())
	return file{name: tmp.Name(), isTemp: true}, nil
}

func offerFile(fname string, bufSize int) (file, error) {
	finfo, err := os.Stat(fname)
	if err != nil {
		return file{}, err
	}
	if finfo.IsDir() {
		return file{}, fmt.Errorf("%s: %w", fname, errIsDir)
	}
	if finfo.Size() > int64(bufSize) {
		return file{name: fname}, nil
	}
	fp, err := os.Open(fname)
	if err != nil {
		return file{}, err
	}
	defer fp.Close()
	buf, err := tryReadAll(fp, bufSize)
	if err != nil {
		return file{}, err
	}
	return file{name: fname, buf: buf, isBuffered: true}, nil
}

func tryReadAll(r io.Reader, bufSize int) ([]byte, error) {
	buf := make([]byte, bufSize)
	n, err := io.ReadFull(r, buf)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || (errors.Is(err, io.EOF) && n == 0) {
			err = nil
		}
		return buf[:n], err
	}
	buf2 := make([]byte, 1)
	n2, err := r.Read(buf2)
	if n2 > 0 {
		buf = append(buf, buf2[:n2]...)
		n += n2
		if err == nil {
			err = errTooBig
		}
	}
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return buf[:n], err
}

func (f file) reader() (io.Reader, error) {
	if f.isBuffered {
		return bytes.NewReader(f.buf), nil
	}
	fp, err := os.Open(f.name)
	if err != nil {
		return nil, err
	}
	return fp, nil
}

func wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL)

		w.Header().Add("Server", fmt.Sprintf("%s %s", progName, progVersion))

		h.ServeHTTP(w, r)
	})
}

func sendFile(f file) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rd, err := f.reader()
		if err != nil {
			httpSendError(w, err, 500)
			return
		}
		httpCopy(w, rd)
	}
}

func sendChecksum(f file, hashNew func() hash.Hash) http.HandlerFunc {
	var once sync.Once
	var sum string
	return func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			rd, err := f.reader()
			if err != nil {
				log.Println(err)
				return
			}
			h := hashNew()
			if _, err := io.Copy(h, rd); err != nil {
				log.Println(err)
				return
			}
			sum = fmt.Sprintf("%x  %s\n", h.Sum(nil), path.Base(f.name))
		})
		if sum == "" {
			httpSendError(w, fmt.Errorf("sendChecksum: %w", errEmptySum), 500)
			return
		}
		httpCopy(w, strings.NewReader(sum))
	}
}

func sendError(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpSendError(w, nil, code)
	}
}

func httpSendError(w http.ResponseWriter, err error, code int) {
	if err != nil {
		log.Println(err)
	}
	http.Error(w, fmt.Sprintf("%d %s", code, http.StatusText(code)), code)
}

func httpCopy(w http.ResponseWriter, rd io.Reader) {
	if _, err := io.Copy(w, rd); err != nil {
		log.Println(err)
		// it's too late to send 500 or any other status.
	}
}
