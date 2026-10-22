package leveldb

import (
	"errors"
	"io"
)

func ReadVarint32(r io.ByteReader) (uint32, error) {
	var res uint32
	var shift uint
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		res |= uint32(b&0x7f) << shift
		if (b & 0x80) == 0 {
			break
		}
		shift += 7
		if shift >= 35 {
			return 0, errors.New("varint32 overflow")
		}
	}
	return res, nil
}
