// See license file for copyright and license details.

package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
)

//go:embed upload.html
var uploadPage []byte

func main() {
	flagFname := flag.String("f", "", "filename for content disposition header")
	flagNReqs := flag.Uint("n", 1, "number of requests allowed")
	flagPort := flag.Uint("p", 8080, "server port")
	flagReceive := flag.Bool("r", false, "receive mode")
	flagUrl := flag.Bool("u", false, "print URL after server starts listening")
	flag.Parse()

	if flag.NArg() > 1 {
		die("too many files, use zip or tar")
	}

	fpath := "-"
	if flag.NArg() == 1 {
		fpath = flag.Args()[0]
	}

	done := make(chan bool)
	var handler http.HandlerFunc
	if *flagReceive {
		if *flagFname != "" {
			fpath = *flagFname
		}
		if fpath == "-" && *flagNReqs > 1 {
			die("can't receive to stdout more than once")
		}
		handler = limitReqs("POST", *flagNReqs, done, receive(fpath))
	} else {
		if fpath == "-" && *flagNReqs > 1 {
			die("can't offer stdin more than once")
		}
		handler = limitReqs("GET", *flagNReqs, done, offer(fpath, *flagFname))
	}

	http.HandleFunc("/", handler)
	srv := http.Server{Addr: fmt.Sprintf(":%d", *flagPort)}

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		die(err.Error())
	}
	if *flagUrl {
		printURL(ln.Addr().(*net.TCPAddr).Port)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-done:
		case <-sig:
		}
		if err := srv.Shutdown(context.Background()); err != nil {
			printError(err)
		}
		done <- true
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		die(err.Error())
	}
	<-done
}

func die(reason string) {
	printError(errors.New(reason))
	os.Exit(1)
}

func printError(err error) {
	fmt.Fprintf(os.Stderr, "error: %s\n", err)
}

func printURL(port int) {
	host := "localhost"
	// https://stackoverflow.com/a/37382208/13527856
	if conn, err := net.Dial("udp", "255.255.255.255:99"); err == nil {
		host = conn.LocalAddr().(*net.UDPAddr).IP.String()
		defer conn.Close()
	}
	fmt.Fprintf(os.Stderr, "http://%s:%d\n", host, port) // don't pollute stdout
}

func writeStatusPage(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!DOCTYPE html>\n<h1>%s</h1>\n", http.StatusText(status))
}

func limitReqs(method string, n uint, done chan bool, next http.HandlerFunc) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			next.ServeHTTP(w, r)
			return
		}

		mu.Lock()
		if n == 0 {
			mu.Unlock()
			writeStatusPage(w, http.StatusServiceUnavailable)
			return
		}
		n--
		if n == 0 {
			defer func() { done <- true }()
		}
		mu.Unlock()

		next.ServeHTTP(w, r)
	}
}

func offer(fpath, fname string) http.HandlerFunc {
	if fpath != "-" && fname == "@" {
		fname = fpath
	}
	if fname != "" {
		fname = filepath.Base(fname)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writeStatusPage(w, http.StatusMethodNotAllowed)
			return
		}

		f := os.Stdin
		if fpath != "-" {
			var err error
			f, err = os.Open(fpath)
			if err != nil {
				printError(err)
				writeStatusPage(w, http.StatusInternalServerError)
				return
			}
		}
		defer f.Close()

		if fname != "" {
			w.Header().Add("Content-Disposition", "attachment; filename="+fname)
		}
		if _, err := io.Copy(w, f); err != nil {
			printError(err)
			return
		}
	}
}

func receive(fpath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write(uploadPage)
			return
		}
		if r.Method != "POST" {
			writeStatusPage(w, http.StatusMethodNotAllowed)
			return
		}

		mr, err := r.MultipartReader()
		if err != nil {
			printError(err)
			writeStatusPage(w, http.StatusBadRequest)
			return
		}

		f := os.Stdout
		if fpath != "-" {
			var err error
			f, err = os.CreateTemp(".", "offer-receive-")
			if err != nil {
				printError(err)
				writeStatusPage(w, http.StatusInternalServerError)
				return
			}
		}
		defer f.Close()

		partfname := ""
		for {
			part, err := mr.NextPart()
			if err != nil {
				if errors.Is(err, io.EOF) {
					writeStatusPage(w, http.StatusOK)
					break
				}
				printError(err)
				writeStatusPage(w, http.StatusBadRequest)
				break
			}

			if fpath == "@" && partfname == "" {
				partfname = part.FileName()
			}

			if _, err := io.Copy(f, part); err != nil {
				printError(err)
				writeStatusPage(w, http.StatusInternalServerError)
				break
			}
		}

		fname := fpath
		if fname == "-" {
			return
		}
		if fname == "@" {
			if partfname == "" {
				printError(fmt.Errorf("content disposition filename missing"))
				return
			}
			fname = partfname
		}
		if err := safeRename(f.Name(), fname); err != nil {
			printError(err)
		}
	}
}

func safeRename(oldpath, newpath string) error {
	fpath := newpath
	for i := 1; i < math.MaxInt32; i++ {
		err := os.Link(oldpath, fpath)
		if err == nil {
			return os.Remove(oldpath)
		}
		if errors.Is(err, fs.ErrExist) {
			fpath = fmt.Sprintf("%s-%d", newpath, i)
			continue
		}
		return err
	}
	return fmt.Errorf("safe rename %q %q: %w", oldpath, newpath, fs.ErrExist)
}
