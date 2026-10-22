package v8

import (
	"bytes"
	"encoding/binary"
	"unicode/utf16"

	"claude-switch/pkg/leveldb"
)

func DecodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	u16s := make([]uint16, len(b)/2)
	for i := range u16s {
		u16s[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16s))
}

func ParseV8String(val []byte, startPos int) (string, int) {
	pos := startPos
	for pos < len(val) && val[pos] == 0 {
		pos++
	}
	if pos >= len(val) {
		return "", pos
	}

	tag := val[pos]
	if tag == '"' { // OneByteString (Latin1/ASCII/UTF-8)
		reader := bytes.NewReader(val[pos+1:])
		l, err := leveldb.ReadVarint32(reader)
		if err != nil {
			return "", pos + 1
		}
		consumed := int(reader.Size()) - reader.Len()
		end := pos + 1 + consumed + int(l)
		if end <= len(val) {
			str := string(val[pos+1+consumed : end])
			return str, end
		}
	} else if tag == 'c' { // TwoByteString (UTF-16LE)
		reader := bytes.NewReader(val[pos+1:])
		l, err := leveldb.ReadVarint32(reader)
		if err != nil {
			return "", pos + 1
		}
		consumed := int(reader.Size()) - reader.Len()
		// l represents byte length in V8 serializer
		end := pos + 1 + consumed + int(l)
		if end <= len(val) {
			str := DecodeUTF16LE(val[pos+1+consumed : end])
			return str, end
		}
	}
	return "", pos + 1
}

func FindV8Field(val []byte, fieldKey string) string {
	needle := []byte(fieldKey)
	idx := bytes.Index(val, needle)
	if idx == -1 {
		return ""
	}
	res, _ := ParseV8String(val, idx+len(needle))
	return res
}
