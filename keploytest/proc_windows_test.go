package keploytest

import (
	"fmt"
	"testing"
	"unicode/utf16"
	"unsafe"
)

// unicodeStringIn reads a UNICODE_STRING's text only from the buffer that
// holds it: a buffer shorter than the header, text before the buffer or past
// its end, or an odd byte length is no text.
func TestUnicodeStringInReadsOnlyItsBuffer(t *testing.T) {
	hdr := int(unsafe.Sizeof(unicodeString{}))
	text := utf16.Encode([]rune("abc"))
	for _, tc := range []struct {
		name string
		// The header is at big[at], naming n bytes at big[from]; the
		// answer read is big[at:end].
		at, from, n, end int
		want             string
		ok               bool
	}{
		{"text after the header", 0, hdr, 6, 64, "abc", true},
		{"no text", 0, hdr, 0, 64, "", true},
		{"an odd length", 0, hdr, 5, 64, "", false},
		{"text past the end", 0, hdr, 6, hdr + 4, "", false},
		{"text before the buffer", 24, 16, 8, 64, "", false},
		{"a buffer shorter than the header", 0, 2, 4, hdr - 1, "", false},
	} {
		big := make([]byte, 64)
		for i, u := range text {
			big[tc.from+2*i], big[tc.from+2*i+1] = byte(u), byte(u>>8)
		}
		us := (*unicodeString)(unsafe.Pointer(&big[tc.at]))
		us.Length, us.MaximumLength = uint16(tc.n), uint16(tc.n)
		us.Buffer = uintptr(unsafe.Pointer(&big[tc.from]))
		got, ok := func() (got string, ok bool) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: read outside the buffer: %v", tc.name, r)
				}
			}()
			return unicodeStringIn(big[tc.at:tc.end])
		}()
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: %q, %v, want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// commandLineFrom reads a command line only from an answer of success that
// holds a string in its buffer, and asks again only for more room.
func TestCommandLineFromReadsOnlyASuccessHoldingItsString(t *testing.T) {
	const accessDenied = 0xC0000022
	hdr := int(unsafe.Sizeof(unicodeString{}))
	// answer is one answer of the system: its status and the size it says
	// the answer needs; with text, "go test -p 1" is put in the buffer as a
	// UNICODE_STRING (its header at the start, its text after it).
	type answer struct {
		status uintptr
		need   uint32
		text   bool
	}
	put := func(buf []byte) {
		u := utf16.Encode([]rune("go test -p 1"))
		for i, c := range u {
			buf[hdr+2*i], buf[hdr+2*i+1] = byte(c), byte(c>>8)
		}
		us := (*unicodeString)(unsafe.Pointer(&buf[0]))
		us.Length, us.MaximumLength = uint16(2*len(u)), uint16(2*len(u))
		us.Buffer = uintptr(unsafe.Pointer(&buf[hdr]))
	}
	mismatch := func(need uint32) answer { return answer{statusInfoLengthMismatch, need, false} }
	for _, tc := range []struct {
		name    string
		answers []answer
		want    []string
		ok      bool
		// sizes is the length of the buffer each question was asked with.
		sizes []int
	}{
		{"a success holding the string", []answer{{0, 0, true}},
			[]string{"go", "test", "-p", "1"}, true, []int{512}},
		{"a status that is not success, the buffer holding a string", []answer{{accessDenied, 0, true}},
			nil, false, []int{512}},
		{"a success holding no string", []answer{{0, 0, false}},
			nil, false, []int{512}},
		{"a command line longer than the buffer, asked again with the room it needs",
			[]answer{mismatch(2048), {0, 0, true}},
			[]string{"go", "test", "-p", "1"}, true, []int{512, 2048}},
		{"a mismatch asking for no more room than the buffer has", []answer{mismatch(100), {0, 0, true}},
			nil, false, []int{512}},
		{"four mismatches", []answer{mismatch(1024), mismatch(2048), mismatch(4096), mismatch(8192), {0, 0, true}},
			nil, false, []int{512, 1024, 2048, 4096}},
	} {
		var sizes []int
		got, ok := commandLineFrom(func(buf []byte, need *uint32) uintptr {
			sizes = append(sizes, len(buf))
			if len(sizes) > len(tc.answers) {
				t.Errorf("%s: asked %d times, more than it was answered", tc.name, len(sizes))
				return accessDenied
			}
			a := tc.answers[len(sizes)-1]
			if a.text {
				put(buf)
			}
			*need = a.need
			return a.status
		})
		if ok != tc.ok || fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tc.want) {
			t.Errorf("%s: %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
		if fmt.Sprint(sizes) != fmt.Sprint(tc.sizes) {
			t.Errorf("%s: asked with buffers of %v bytes, want %v", tc.name, sizes, tc.sizes)
		}
	}
}
