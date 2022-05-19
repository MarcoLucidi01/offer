// See license file for copyright and license details.

package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
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
	srv := http.Server{Addr: fmt.Sprintf(":%d", *flagPort)}

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		die(err.Error())
	}

	if *flagUrl {
		if err := printURL(srv.Addr); err != nil {
			// don't die, the server is already listening, this
			// error should never happen.
			printError(err)
		}
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

func printURL(addrStr string) error {
	addr, err := net.ResolveTCPAddr("tcp", addrStr)
	if err != nil {
		return err
	}

	// https://stackoverflow.com/a/37382208/13527856
	conn, err := net.Dial("udp", "255.255.255.255:99")
	if err != nil {
		return err
	}
	defer conn.Close()

	ip := conn.LocalAddr().(*net.UDPAddr).IP.String()
	// don't pollute stdout
	fmt.Fprintf(os.Stderr, "http://%s:%d\n", ip, addr.Port)
	return nil
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
				printError(err)
				sendStatusPage(w, http.StatusInternalServerError)
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
			sendStatusPage(w, http.StatusMethodNotAllowed)
			return
		}

		mr, err := r.MultipartReader()
		if err != nil {
			printError(err)
			sendStatusPage(w, http.StatusBadRequest)
			return
		}

		f := os.Stdout
		if fpath != "-" {
			var err error
			f, err = os.Create(fpath)
			if err != nil {
				printError(err)
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
				printError(err)
				sendStatusPage(w, http.StatusBadRequest)
				return
			}
			if _, err := io.Copy(f, part); err != nil {
				printError(err)
				sendStatusPage(w, http.StatusInternalServerError)
				return
			}
		}
	}
}
