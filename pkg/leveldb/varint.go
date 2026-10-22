package leveldb

import (
	"errors"
	"io"
)

// ReadVarint32 reads a 32-bit unsigned varint from r.
// Enforces a strict 5-byte maximum and rejects continuation bits or overflow bits on the 5th byte.
func ReadVarint32(r io.ByteReader) (uint32, error) {
	var res uint32
	var shift uint
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if i == 4 {
			// 5th byte: only 4 bits (0..3) can be used for uint32, and continuation bit must be 0
			if b > 0x0f {
				return 0, errors.New("varint32 overflow: 5th byte has bits exceeding uint32 or continuation bit")
			}
			res |= uint32(b) << shift
			return res, nil
		}
		res |= uint32(b&0x7f) << shift
		if (b & 0x80) == 0 {
			return res, nil
		}
		shift += 7
	}
	return 0, errors.New("varint32 overflow: exceeds 5 bytes")
}

// readVarint64 reads a 64-bit unsigned varint from r.
// Enforces a strict 10-byte maximum and rejects continuation bits or overflow bits on the 10th byte.
func readVarint64(r io.ByteReader) (uint64, error) {
	var res uint64
	var shift uint
	for i := 0; i < 10; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if i == 9 {
			// 10th byte: only 1 bit (bit 0) can be used for uint64, and continuation bit must be 0
			if b > 1 {
				return 0, errors.New("varint64 overflow: 10th byte has bits exceeding uint64 or continuation bit")
			}
			res |= uint64(b) << shift
			return res, nil
		}
		res |= uint64(b&0x7f) << shift
		if (b & 0x80) == 0 {
			return res, nil
		}
		shift += 7
	}
	return 0, errors.New("varint64 overflow: exceeds 10 bytes")
}
