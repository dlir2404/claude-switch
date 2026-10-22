package leveldb

import (
	"bytes"
	"strings"
	"testing"
)

func TestSnappyLiterals(t *testing.T) {
	// Test 3-byte literal header: tag = 62 << 2 = 248 = 0xf8
	// Length = 65536 + 100 = 65636 (0x010064)
	rawLen := 65636
	data := bytes.Repeat([]byte("X"), rawLen)

	var snappyBuf bytes.Buffer
	writeVarint32(&snappyBuf, uint32(rawLen))
	snappyBuf.WriteByte(62 << 2)
	snappyBuf.WriteByte(0x63)
	snappyBuf.WriteByte(0x00)
	snappyBuf.WriteByte(0x01)
	snappyBuf.Write(data)

	out, err := DecompressSnappy(snappyBuf.Bytes())
	if err != nil {
		t.Fatalf("DecompressSnappy failed for 3-byte literal: %v", err)
	}
	if len(out) != rawLen {
		t.Errorf("expected len %d, got %d", rawLen, len(out))
	}
	if !bytes.Equal(out, data) {
		t.Errorf("decompressed data mismatch")
	}
}

func TestSnappyLengthVerificationAndBounds(t *testing.T) {
	// 1. Enormous declared length > 64MB limit
	var hugeBuf bytes.Buffer
	writeVarint64(&hugeBuf, 1024*1024*1024) // 1 GB
	hugeBuf.WriteString("small payload")
	_, err := DecompressSnappy(hugeBuf.Bytes())
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed limit") {
		t.Errorf("expected error for >64MB declared length, got: %v", err)
	}

	// 2. Declared length larger than actual decompressed output
	var bufGreater bytes.Buffer
	writeVarint32(&bufGreater, 50) // declares 50 bytes
	bufGreater.WriteByte(byte(4) << 2) // literal of 5 bytes
	bufGreater.WriteString("hello")
	_, err = DecompressSnappy(bufGreater.Bytes())
	if err == nil || !strings.Contains(err.Error(), "length mismatch") {
		t.Errorf("expected length mismatch error when output < declared, got: %v", err)
	}

	// 3. Declared length smaller than actual decompressed output
	var bufSmaller bytes.Buffer
	writeVarint32(&bufSmaller, 3) // declares only 3 bytes
	bufSmaller.WriteByte(byte(4) << 2) // literal of 5 bytes
	bufSmaller.WriteString("hello")
	_, err = DecompressSnappy(bufSmaller.Bytes())
	if err == nil || !strings.Contains(err.Error(), "exceeds declared uncompressed length") {
		t.Errorf("expected error when output > declared, got: %v", err)
	}

	// 4. Valid block with exact matching length
	var validBuf bytes.Buffer
	validData := []byte("hello world from snappy test")
	writeVarint32(&validBuf, uint32(len(validData)))
	validBuf.WriteByte(byte(len(validData)-1) << 2) // literal
	validBuf.Write(validData)
	out, err := DecompressSnappy(validBuf.Bytes())
	if err != nil {
		t.Fatalf("unexpected error for valid block: %v", err)
	}
	if !bytes.Equal(out, validData) {
		t.Errorf("expected %q, got %q", validData, out)
	}
}
