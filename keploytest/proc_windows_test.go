package keploytest

import (
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
