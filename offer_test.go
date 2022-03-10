package main

import (
	"errors"
	"strings"
	"testing"
)

func TestTryReadAll(t *testing.T) {
	var tests = []struct {
		input   string
		bufSize int
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
		if len(buf) > test.bufSize+1 {
			t.Fatalf("%d: len(buf) > bufSize+1: %d > %d", i, len(buf), test.bufSize)
		}
		for j, b := range buf {
			if test.input[j] != b {
				t.Fatalf("%d: input[%d] != buf[%d]: %q != %q", i, j, j, test.input[j], buf[j])
			}
		}
	}
}
