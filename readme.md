offer
=====

offer a file on the local network via http.

this is a quick and dirty program i use to exchange files with basically
any device on my LAN that has a web browser.

it's like [`qrcp`][0] but worse.

install
-------

    $ go install github.com/MarcoLucidi01/offer@latest

or clone, build and move in your `$PATH`:

    $ git clone https://github.com/MarcoLucidi01/offer
    ...
    $ cd offer
    $ go build
    $ mv offer ~/bin

usage
-----

    $ offer -h
    Usage of offer:
      -f string
        	filename for content disposition header
      -n uint
        	number of requests allowed (default 1)
      -p uint
        	server port (default 8080)
      -r	receive mode
      -u	print URL after server starts listening

examples
--------

offer a file:

    $ offer hello.txt

offer a file multiple times (5 in this case):

    $ offer -n 5 hello.txt

offer a file with [content disposition][1] attachment header:

    $ offer -f @ hello.txt

(`@` means use the filename (`hello.txt` in this case) as content disposition filename)

offer `stdin`:

    $ echo hello world | offer

(`stdin` can't be offered multiple times)

offer multiple files:

    $ tar -czf - foo.txt bar.pdf baz.png | offer -f foo.tar.gz

(you can of course use `zip` or whatever)

offer a folder:

    $ tar -czf - ~/music | offer -f music.tar.gz

receive a file:

    $ offer -r -u hello.txt
    http://192.168.100.123:8080

(only *one* `POST` request will be allowed in receive mode. a `GET` request will
return a basic upload page)

receive multiple files (using `tar`):

    $ offer -r | tar -xvf -

use content disposition filename as filename for received file:

    $ offer -r -f @

[license][2]

[0]: https://github.com/claudiodangelis/qrcp
[1]: https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Disposition
[2]: license
