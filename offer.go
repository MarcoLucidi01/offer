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
	defaultTimeout = 0              // 0 means no timeout.
	defaultNReqs   = 0              // 0 means unlimited requests.
)

var (
	progVersion = "vX.Y.Z-dev" // set with -ldflags at build time.

	errIsDir           = errors.New("is a directory")
	errTooBig          = errors.New("too big")
	errTooManyFiles    = errors.New("too many files")
	errEmptySum        = errors.New("empty checksum")
	errInvalidUserPass = errors.New("invalid user:password")

	flagAddress  = flag.String("a", defaultAddr, "server address:port")
	flagBufSize  = flag.Uint("b", defaultBufSize, "buffer size in bytes")
	flagFilename = flag.String("f", "", "custom filename for Content-Disposition header")
	flagKeep     = flag.Bool("k", false, "don't remove stored stdin file")
	flagLog      = flag.Bool("log", false, "enable verbose logging")
	flagNReqs    = flag.Uint("n", defaultNReqs, "shutdown server after n requests")
	flagNoDisp   = flag.Bool("nd", false, "no disposition, don't send Content-Disposition header")
	flagStream   = flag.Bool("s", false, "stream mode, don't store stdin in a file, allow only 1 request")
	flagTempDir  = flag.String("tempdir", os.TempDir(), "temporary directory for storing stdin in a file")
	flagTimeout  = flag.Duration("t", defaultTimeout, "timeout for automatic server shutdown")
	flagUserPass = flag.String("u", "", "user:password for basic authentication")
)

// TODO rename to payload?
type file struct {
	path       string
	name       string
	buf        []byte
	stream     io.Reader
	isBuffered bool
	isTemp     bool
	isStream   bool
}

type statusRespWriter struct {
	http.ResponseWriter
	status int
}

func main() {
	flag.Parse()

	// TODO check flags values before using them, add a checkFlags() func.
	// TODO -r flag for receiving a file? i.e. receive an offer eheh.

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
	user, pass, err := parseUserPass(*flagUserPass)
	if err != nil {
		return err
	}

	var f file
	switch {
	case len(flag.Args()) == 0 || (len(flag.Args()) == 1 && flag.Args()[0] == "-"):
		if f, err = offerStdin(*flagBufSize, *flagTempDir, *flagStream); err != nil {
			return err
		}
	case len(flag.Args()) == 1:
		if f, err = offerFile(flag.Args()[0], *flagBufSize); err != nil {
			return err
		}
	default:
		return errTooManyFiles
	}
	if *flagFilename != "" {
		f.name = path.Base(*flagFilename)
	}
	if f.isStream {
		*flagNReqs = 1
	}

	done := make(chan struct{})
	srv := http.Server{
		Addr:    *flagAddress,
		Handler: logReqs(commonRespHeaders(basicAuth(user, pass, limitNReqs(*flagNReqs, done, http.DefaultServeMux)))),
	}
	http.HandleFunc("/", sendFile(f, !*flagNoDisp))
	http.HandleFunc("/checksums", sendError(404))
	http.HandleFunc("/checksums/", sendError(404))
	http.HandleFunc("/checksums/md5", sendChecksum(f, md5.New))
	http.HandleFunc("/checksums/sha1", sendChecksum(f, sha1.New))
	http.HandleFunc("/checksums/sha256", sendChecksum(f, sha256.New))
	http.HandleFunc("/checksums/sha512", sendChecksum(f, sha512.New))

	waitConns := make(chan struct{})
	go func() {
		timer := time.NewTimer(*flagTimeout)
		if *flagTimeout == 0 && !timer.Stop() {
			<-timer.C // 0 means no timeout, drain the channel.
		}

		sigch := make(chan os.Signal, 1)
		signal.Notify(sigch, os.Interrupt, syscall.SIGTERM)

		select {
		case sig := <-sigch:
			log.Printf("got signal %q, shutting down", sig)
		case <-timer.C:
			log.Println("timeout expired, shutting down")
		case <-done:
			log.Println("max number of requests fulfilled, shutting down")
		}

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
		log.Printf("removing %s", f.path)
		return os.Remove(f.path)
	}
	return nil
}

func parseUserPass(s string) (string, string, error) {
	if s == "" {
		return "", "", nil
	}
	cred := strings.SplitN(s, ":", 2)
	if len(cred) != 2 || cred[0] == "" || cred[1] == "" {
		return "", "", errInvalidUserPass
	}
	return cred[0], cred[1], nil
}

func offerStdin(bufSize uint, tempDir string, stream bool) (file, error) {
	if stream {
		return file{path: "-", name: defaultStdinName(), stream: os.Stdin, isStream: true}, nil
	}
	buf, err := tryReadAll(os.Stdin, bufSize)
	if err == nil {
		return file{path: "-", name: defaultStdinName(), buf: buf, isBuffered: true}, nil
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
	return file{path: tmp.Name(), name: path.Base(tmp.Name()), isTemp: true}, nil
}

func defaultStdinName() string {
	return fmt.Sprintf("%s-%d", progName, time.Now().Unix())
}

func offerFile(fpath string, bufSize uint) (file, error) {
	finfo, err := os.Stat(fpath)
	if err != nil {
		return file{}, err
	}
	if finfo.IsDir() {
		return file{}, fmt.Errorf("%s: %w", fpath, errIsDir)
	}
	if uint(finfo.Size()) > bufSize {
		return file{path: fpath, name: path.Base(fpath)}, nil
	}
	fp, err := os.Open(fpath)
	if err != nil {
		return file{}, err
	}
	defer fp.Close()
	buf, err := tryReadAll(fp, bufSize)
	if err != nil {
		return file{}, err
	}
	return file{path: fpath, name: path.Base(fpath), buf: buf, isBuffered: true}, nil
}

func tryReadAll(r io.Reader, bufSize uint) ([]byte, error) {
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
	if f.isStream {
		return f.stream, nil
	}
	if f.isBuffered {
		return bytes.NewReader(f.buf), nil
	}
	fp, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	return fp, nil
}

func (w *statusRespWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func logReqs(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("--> %s %s %s", r.RemoteAddr, r.Method, r.URL)
		sw := &statusRespWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(sw, r)
		log.Printf("<-- %s %s %s %d %s", r.RemoteAddr, r.Method, r.URL, sw.status, http.StatusText(sw.status))
	})
}

func commonRespHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Server", fmt.Sprintf("%s %s", progName, progVersion))
		h.ServeHTTP(w, r)
	})
}

// TODO count only successful (200 OK) responses?
func limitNReqs(n uint, done chan struct{}, h http.Handler) http.Handler {
	if n == 0 { // 0 means unlimited requests.
		return h
	}
	var mu sync.Mutex
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if n == 0 {
			mu.Unlock()
			httpSendError(w, nil, 503)
			return
		}
		n--
		mu.Unlock()
		h.ServeHTTP(w, r)
		mu.Lock()
		if n == 0 {
			once.Do(func() { close(done) })
		}
		mu.Unlock()
	})
}

func basicAuth(user, pass string, h http.Handler) http.Handler {
	if user == "" || pass == "" { // empty user or pass means no auth.
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if ok && u == user && p == pass {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Add("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q, charset="utf-8"`, progName))
		httpSendError(w, nil, 401)
	})
}

func sendFile(f file, disp bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rd, err := f.reader()
		if err != nil {
			httpSendError(w, err, 500)
			return
		}
		if disp {
			w.Header().Add("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.name))
		}
		if f.isStream {
			// in stream mode we can't seek the reader to get
			// content size or serve ranges like http.ServeContent() does.
			// NOTE: *os.File is a io.ReadSeeker but os.Stdin won't seek.
			httpCopy(w, rd)
			return
		}
		// TODO use proper modtime?
		http.ServeContent(w, r, f.name, time.Time{}, rd.(io.ReadSeeker))
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
			sum = fmt.Sprintf("%x  %s\n", h.Sum(nil), f.name)
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
