package main

import (
	"errors"
	"strings"
	"testing"
)

func TestTryReadAll(t *testing.T) {
	var tests = []struct {
		input   string
		bufSize uint
		err     error
	}{
		{"", 0, nil},
		{"", 1, nil},
		{"", 2, nil},
		{"", 3, nil},
		{"a", 1, nil},
		{"aa", 1, errTooBig},
		{"aaa", 1, errTooBig},
		{"aaaa", 1, errTooBig},
		{"b", 2, nil},
		{"bb", 2, nil},
		{"bbb", 2, errTooBig},
		{"bbbb", 2, errTooBig},
		{"foobar", 8, nil},
		{"foobar", 7, nil},
		{"foobar", 6, nil},
		{"foobar", 5, errTooBig},
		{"foobar", 4, errTooBig},
		{"foobar", 3, errTooBig},
		{"foobar", 2, errTooBig},
		{"foobar", 1, errTooBig},
		{"foobar", 0, errTooBig},
	}

	for i, test := range tests {
		buf, err := tryReadAll(strings.NewReader(test.input), test.bufSize)
		if !errors.Is(err, test.err) {
			t.Fatalf(`%d: expecting "%v" error but got "%v"`, i, test.err, err)
		}
		if err == nil && len(test.input) != len(buf) {
			t.Fatalf("%d: len(input) != len(buf): %d != %d", i, len(test.input), len(buf))
		}
		if uint(len(buf)) > test.bufSize+1 {
			t.Fatalf("%d: len(buf) > bufSize+1: %d > %d", i, len(buf), test.bufSize)
		}
		for j, b := range buf {
			if test.input[j] != b {
				t.Fatalf("%d: input[%d] != buf[%d]: %q != %q", i, j, j, test.input[j], buf[j])
			}
		}
	}
}

func TestParseUserPass(t *testing.T) {
	var tests = []struct {
		s    string
		user string
		pass string
		err  error
	}{
		{"", "", "", nil},
		{":", "", "", errInvalidUserPass},
		{"foo", "", "", errInvalidUserPass},
		{"foo:", "", "", errInvalidUserPass},
		{":foo", "", "", errInvalidUserPass},
		{"f:b", "f", "b", nil},
		{"foo:bar", "foo", "bar", nil},
		{"foo:bar:baz", "foo", "bar:baz", nil},
		{"user with spaces:password", "user with spaces", "password", nil},
		{"Aladdin:open sesame", "Aladdin", "open sesame", nil},
	}

	for i, test := range tests {
		user, pass, err := parseUserPass(test.s)
		if !errors.Is(err, test.err) {
			t.Fatalf(`%d: %q: expecting "%v" error but got "%v"`, i, test.s, test.err, err)
		}
		if user != test.user {
			t.Fatalf("%d: %q: expecting user %q but got %q", i, test.s, test.user, user)
		}
		if pass != test.pass {
			t.Fatalf("%d: %q: expecting password %q but got %q", i, test.s, test.pass, pass)
		}
	}
}
