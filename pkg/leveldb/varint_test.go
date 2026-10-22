package leveldb

import (
	"bytes"
	"testing"
)

func TestReadVarint32(t *testing.T) {
	tests := []struct {
		input    []byte
		expected uint32
	}{
		{[]byte{0x00}, 0},
		{[]byte{0x01}, 1},
		{[]byte{0x7f}, 127},
		{[]byte{0x80, 0x01}, 128},
		{[]byte{0xff, 0x01}, 255},
		{[]byte{0x80, 0x02}, 256},
		{[]byte{0x9e, 0xa7, 0x05}, 86942},
	}

	for _, tt := range tests {
		r := bytes.NewReader(tt.input)
		res, err := ReadVarint32(r)
		if err != nil {
			t.Fatalf("unexpected error for %v: %v", tt.input, err)
		}
		if res != tt.expected {
			t.Errorf("input %v: got %d, want %d", tt.input, res, tt.expected)
		}
	}
}
