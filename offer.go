// See license file for copyright and license details.

package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
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
	flagAddr := flag.String("a", ":8080", "server address:port")
	flagFname := flag.String("f", "", "filename for content disposition header")
	flagNReqs := flag.Uint("n", 1, "number of requests allowed")
	flagReceive := flag.Bool("r", false, "receive mode")
	flag.Parse()

	if flag.NArg() > 1 {
		die("too many files, use zip or tar to offer multiple files")
	}

	fpath := "-"
	if flag.NArg() == 1 {
		fpath = flag.Args()[0]
	}

	done := make(chan bool)
	var handler http.HandlerFunc
	if *flagReceive {
		if *flagNReqs > 1 {
			die("can't receive more than one file")
		}
		if *flagFname != "" {
			fpath = *flagFname
		}
		handler = limitReqs("POST", 1, done, receive(fpath))
	} else {
		if fpath == "-" && *flagNReqs > 1 {
			die("can't offer stdin more than once")
		}
		if fpath != "-" && *flagFname == "@" {
			*flagFname = fpath
		}
		if *flagFname != "" {
			*flagFname = filepath.Base(*flagFname)
		}
		handler = limitReqs("GET", *flagNReqs, done, offer(fpath, *flagFname))
	}

	http.HandleFunc("/", handler)
	srv := http.Server{Addr: *flagAddr}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-done:
		case <-sig:
		}
		if err := srv.Shutdown(context.Background()); err != nil {
			errmsg(err.Error())
		}
		done <- true
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		die(err.Error())
	}
	<-done
}

func die(reason string) {
	errmsg(reason)
	os.Exit(1)
}

func errmsg(msg ...interface{}) {
	fmt.Fprint(os.Stderr, "error: ")
	fmt.Fprintln(os.Stderr, msg...)
}

func sendStatusPage(w http.ResponseWriter, status int) {
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
			sendStatusPage(w, http.StatusServiceUnavailable)
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
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			sendStatusPage(w, http.StatusMethodNotAllowed)
			return
		}

		f := os.Stdin
		if fpath != "-" {
			var err error
			f, err = os.Open(fpath)
			if err != nil {
				errmsg(err.Error())
				sendStatusPage(w, http.StatusInternalServerError)
				return
			}
		}
		defer f.Close()

		if fname != "" {
			w.Header().Add("Content-Disposition", "attachment; filename="+fname)
		}
		if _, err := io.Copy(w, f); err != nil {
			errmsg(err.Error())
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
			sendStatusPage(w, http.StatusMethodNotAllowed)
			return
		}

		mr, err := r.MultipartReader()
		if err != nil {
			errmsg(err.Error())
			sendStatusPage(w, http.StatusBadRequest)
			return
		}

		f := os.Stdout
		if fpath != "-" {
			var err error
			f, err = os.Create(fpath)
			if err != nil {
				errmsg(err.Error())
				sendStatusPage(w, http.StatusInternalServerError)
				return
			}
		}
		defer f.Close()

		for {
			part, err := mr.NextPart()
			if err != nil {
				if errors.Is(err, io.EOF) {
					sendStatusPage(w, http.StatusOK)
					return
				}
				errmsg(err.Error())
				sendStatusPage(w, http.StatusBadRequest)
				return
			}
			if _, err := io.Copy(f, part); err != nil {
				errmsg(err.Error())
				sendStatusPage(w, http.StatusInternalServerError)
				return
			}
		}
	}
}
