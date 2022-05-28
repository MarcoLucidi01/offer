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

    ===

    $ curl http://192.168.100.123:8080
    hello world

offer a file multiple times (3 in this case):

    $ offer -n 3 hello.txt

    ===

    $ curl http://192.168.100.123:8080
    hello world
    $ curl http://192.168.100.123:8080
    hello world
    $ curl http://192.168.100.123:8080
    hello world

offer a file with [content disposition][1] attachment header:

    $ offer -f @ hello.txt

    ===

    $ curl -i http://192.168.100.123:8080
    HTTP/1.1 200 OK
    Content-Disposition: attachment; filename=hello.txt
    Date: Sat, 28 May 2022 18:57:40 GMT
    Content-Length: 12
    Content-Type: text/plain; charset=utf-8

    hello world

(`@` means use the filename (`hello.txt` in this case) as content disposition filename)

offer `stdin`:

    $ echo hello world | offer

    ===

    $ curl http://192.168.100.123:8080
    hello world

(`stdin` can't be offered multiple times)

offer multiple files:

    $ tar -czf - foo.txt bar.pdf baz.png | offer -f foo.tar.gz

    ===

    $ curl -s http://192.168.100.123:8080 | tar -xzf -
    $ ls
    bar.pdf  baz.png  foo.txt

(you can of course use `zip` or whatever)

offer a folder:

    $ tar -czf - ~/music | offer -f music.tar.gz

    ===

    $ curl -s http://192.168.100.123:8080 | tar -xzf -
    $ ls
    eminem
    pink floyd
    the doors
    ...

receive a file:

    $ offer -r -u hello.txt
    http://192.168.100.123:8080
    $ more hello.txt
    hello world

    ===

    $ curl -F 'file=@hello.txt' http://192.168.100.123:8080
    <!DOCTYPE html>
    <h1>OK</h1>

(a `GET` request will return a basic upload page)

    $ curl http://192.168.100.123:8080
    <!DOCTYPE html>
    <form method="POST" action="/" enctype="multipart/form-data">
            <input type="file" name="file" />
            <button type="submbit">upload</button>
    </form>

receive multiple files (using `tar`):

    $ offer -r | tar -xvf -
    foo.txt
    bar.pdf
    baz.png

    ===

    $ tar -cf - foo.txt bar.pdf baz.png | curl -F 'file=@-' http://192.168.100.123:8080

use content disposition filename as filename for received file:

    $ offer -r -f @
    $ more hello.txt
    hello world

    ===

    $ curl -F 'file=@hello.txt' http://192.168.100.123:8080
    <!DOCTYPE html>
    <h1>OK</h1>

[license][2]

[0]: https://github.com/claudiodangelis/qrcp
[1]: https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Disposition
[2]: license
