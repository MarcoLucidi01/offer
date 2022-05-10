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
	"path/filepath"
	"sync"
)

//go:embed upload.html
var uploadPage []byte

//go:embed success.html
var successPage []byte

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
	if fpath == "-" && *flagNReqs > 1 {
		die("can't offer stdin more than once")
	}
	if fpath != "-" && *flagFname == "@" {
		*flagFname = fpath
	}
	if *flagFname != "" {
		*flagFname = filepath.Base(*flagFname)
	}

	done := make(chan bool)
	handler := limitReqs(*flagNReqs, done, offer(fpath, *flagFname))
	if *flagReceive {
		handler = limitReqs(1, done, receive(fpath))
	}
	http.HandleFunc("/", handler)

	srv := http.Server{Addr: *flagAddr}
	go func() {
		<-done
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

func limitReqs(n uint, done chan bool, next http.HandlerFunc) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if n == 0 {
			mu.Unlock()
			http.Error(w, http.StatusText(503), 503)
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
			http.Error(w, http.StatusText(405), 405)
			return
		}

		f := os.Stdin
		if fpath != "-" {
			var err error
			f, err = os.Open(fpath)
			if err != nil {
				errmsg(err.Error())
				http.Error(w, http.StatusText(500), 500)
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
			http.Error(w, http.StatusText(405), 405)
			return
		}

		mr, err := r.MultipartReader()
		if err != nil {
			errmsg(err.Error())
			http.Error(w, http.StatusText(400), 400)
			return
		}

		f := os.Stdout
		if fpath != "-" {
			var err error
			f, err = os.Create(fpath)
			if err != nil {
				errmsg(err.Error())
				http.Error(w, http.StatusText(500), 500)
				return
			}
		}
		defer f.Close()

		for {
			part, err := mr.NextPart()
			if err != nil {
				if errors.Is(err, io.EOF) {
					w.Write(successPage)
					return
				}
				errmsg(err.Error())
				http.Error(w, http.StatusText(400), 400)
				return
			}
			if _, err := io.Copy(f, part); err != nil {
				errmsg(err.Error())
				http.Error(w, http.StatusText(500), 500)
				return
			}
		}
	}
}
