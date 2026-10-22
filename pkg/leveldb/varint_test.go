package leveldb

import (
	"bytes"
	"math"
	"testing"
)

func TestReadVarint32(t *testing.T) {
	validTests := []struct {
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
		{[]byte{0xff, 0xff, 0xff, 0xff, 0x0f}, math.MaxUint32}, // Boundary MaxUint32
	}

	for _, tt := range validTests {
		r := bytes.NewReader(tt.input)
		res, err := ReadVarint32(r)
		if err != nil {
			t.Fatalf("unexpected error for %v: %v", tt.input, err)
		}
		if res != tt.expected {
			t.Errorf("input %v: got %d, want %d", tt.input, res, tt.expected)
		}
	}

	// Error tests
	errTests := [][]byte{
		{0x80}, // truncated
		{0x80, 0x80, 0x80}, // truncated
		{0xff, 0xff, 0xff, 0xff, 0x10}, // 5th byte overflow (0x10 > 0x0f)
		{0xff, 0xff, 0xff, 0xff, 0x20}, // 5th byte overflow
		{0xff, 0xff, 0xff, 0xff, 0x80, 0x01}, // 5th byte has continuation bit
		{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}, // > 5 bytes
	}

	for _, input := range errTests {
		r := bytes.NewReader(input)
		_, err := ReadVarint32(r)
		if err == nil {
			t.Errorf("expected error for invalid varint32 input %v, got nil", input)
		}
	}
}

func TestReadVarint64(t *testing.T) {
	validTests := []struct {
		input    []byte
		expected uint64
	}{
		{[]byte{0x00}, 0},
		{[]byte{0x01}, 1},
		{[]byte{0x7f}, 127},
		{[]byte{0x80, 0x01}, 128},
		{[]byte{0xff, 0x01}, 255},
		{[]byte{0x9e, 0xa7, 0x05}, 86942},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0x0f}, math.MaxUint32},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}, math.MaxUint64}, // Boundary MaxUint64
	}

	for _, tt := range validTests {
		r := bytes.NewReader(tt.input)
		res, err := readVarint64(r)
		if err != nil {
			t.Fatalf("unexpected error for %v: %v", tt.input, err)
		}
		if res != tt.expected {
			t.Errorf("input %v: got %d, want %d", tt.input, res, tt.expected)
		}
	}

	// Error tests
	errTests := [][]byte{
		{0x80}, // truncated
		{0x80, 0x80, 0x80, 0x80, 0x80}, // truncated
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}, // 10th byte overflow (0x02 > 1)
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x10}, // 10th byte overflow
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x80, 0x01}, // 10th byte has continuation bit
		bytes.Repeat([]byte{0x80}, 11), // > 10 bytes
	}

	for _, input := range errTests {
		r := bytes.NewReader(input)
		_, err := readVarint64(r)
		if err == nil {
			t.Errorf("expected error for invalid varint64 input %v, got nil", input)
		}
	}
}
