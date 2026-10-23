package v8

import (
	"testing"
	"unicode/utf8"
)

func TestDecodeUTF16LE(t *testing.T) {
	// "Tiếng Việt" in UTF-16LE bytes:
	// T: 0x54 0x00
	// i: 0x69 0x00
	// ế: 0xbf 0x1e (U+1EBF)
	// n: 0x6e 0x00
	// g: 0x67 0x00
	//  : 0x20 0x00
	// V: 0x56 0x00
	// i: 0x69 0x00
	// ệ: 0xc7 0x1e (U+1EC7)
	// t: 0x74 0x00
	raw := []byte{
		0x54, 0x00,
		0x69, 0x00,
		0xbf, 0x1e,
		0x6e, 0x00,
		0x67, 0x00,
		0x20, 0x00,
		0x56, 0x00,
		0x69, 0x00,
		0xc7, 0x1e,
		0x74, 0x00,
	}

	res := DecodeUTF16LE(raw)
	expected := "Tiếng Việt"
	if res != expected {
		t.Errorf("got %q, want %q", res, expected)
	}
}

func TestParseV8StringLatin1(t *testing.T) {
	// OneByteString tag '"' with Latin-1 byte 0xe9 ('é') and 0xa7 ('§')
	// e.g. "café §6.1"
	// Length = 9 bytes
	raw := []byte{
		'"', 9,
		'c', 'a', 'f', 0xe9, ' ', 0xa7, '6', '.', '1',
	}

	str, end := ParseV8String(raw, 0)
	if end != len(raw) {
		t.Errorf("expected end %d, got %d", len(raw), end)
	}
	expected := "café §6.1"
	if str != expected {
		t.Errorf("got %q, want %q", str, expected)
	}
	if !utf8.ValidString(str) {
		t.Errorf("decoded string %q is not valid UTF-8", str)
	}
}
