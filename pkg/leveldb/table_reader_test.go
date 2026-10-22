package leveldb

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
)

func writeVarint64(buf *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

func buildSyntheticDataBlock(entries []TableEntry) []byte {
	var buf bytes.Buffer
	restarts := []uint32{0}
	var prevKey []byte

	for _, e := range entries {
		var fullKey []byte
		fullKey = append(fullKey, e.UserKey...)
		tag := e.Seq << 8
		if e.IsDelete {
			tag |= 0
		} else {
			tag |= 1
		}
		var tagBytes [8]byte
		binary.LittleEndian.PutUint64(tagBytes[:], tag)
		fullKey = append(fullKey, tagBytes[:]...)

		// Calculate shared
		shared := 0
		for shared < len(prevKey) && shared < len(fullKey) && prevKey[shared] == fullKey[shared] {
			shared++
		}
		unshared := len(fullKey) - shared

		writeVarint32(&buf, uint32(shared))
		writeVarint32(&buf, uint32(unshared))
		writeVarint32(&buf, uint32(len(e.Value)))
		buf.Write(fullKey[shared:])
		buf.Write(e.Value)
		prevKey = fullKey
	}

	// Restarts
	restartsOffset := buf.Len()
	for _, r := range restarts {
		_ = binary.Write(&buf, binary.LittleEndian, r)
	}
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(restarts)))
	_ = restartsOffset
	return buf.Bytes()
}

func TestParseDataBlockValid(t *testing.T) {
	entries := []TableEntry{
		{UserKey: []byte("apple"), Value: []byte("red"), Seq: 1, IsDelete: false},
		{UserKey: []byte("apply"), Value: []byte("job"), Seq: 2, IsDelete: false},
		{UserKey: []byte("banana"), Value: []byte("yellow"), Seq: 3, IsDelete: false},
	}
	data := buildSyntheticDataBlock(entries)

	parsed, err := parseDataBlock(data)
	if err != nil {
		t.Fatalf("parseDataBlock failed on valid block: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(parsed))
	}
	if string(parsed[0].UserKey) != "apple" || string(parsed[0].Value) != "red" {
		t.Errorf("entry 0 mismatch: %v", parsed[0])
	}
	if string(parsed[1].UserKey) != "apply" || string(parsed[1].Value) != "job" {
		t.Errorf("entry 1 mismatch: %v", parsed[1])
	}
	if string(parsed[2].UserKey) != "banana" || string(parsed[2].Value) != "yellow" {
		t.Errorf("entry 2 mismatch: %v", parsed[2])
	}
}

func TestParseDataBlockTruncations(t *testing.T) {
	entries := []TableEntry{
		{UserKey: []byte("apple"), Value: []byte("red"), Seq: 1, IsDelete: false},
		{UserKey: []byte("apply"), Value: []byte("job"), Seq: 2, IsDelete: false},
	}
	validData := buildSyntheticDataBlock(entries)

	// 1. Truncate in restarts array
	_, err := parseDataBlock(validData[:len(validData)-2])
	if err == nil {
		t.Errorf("expected error for truncated block at restarts")
	}

	// 2. Corrupt restarts count (declaring restarts larger than data length)
	corruptRestarts := append([]byte(nil), validData...)
	binary.LittleEndian.PutUint32(corruptRestarts[len(corruptRestarts)-4:], 999999)
	_, err = parseDataBlock(corruptRestarts)
	if err == nil || !strings.Contains(err.Error(), "corrupt restarts") {
		t.Errorf("expected corrupt restarts error, got: %v", err)
	}

	// 3. Truncate at varint header of second entry
	// Find where second entry begins and truncate mid-varint
	restartsOffset := len(validData) - 8 // 1 restart (4B) + numRestarts (4B)
	for cut := restartsOffset - 1; cut > 10; cut -= 2 {
		data := validData[:cut]
		// add valid 4-byte zero restarts count to test header truncation inside entries
		data = append(data, 0, 0, 0, 0)
		_, err := parseDataBlock(data)
		if err == nil {
			t.Errorf("expected error when cutting at offset %d, but got nil error", cut)
		}
	}
}

func TestParseIndexBlockTruncations(t *testing.T) {
	// Build an index block
	var buf bytes.Buffer
	// Entry: shared 0, unshared 4 ("key1"), value 2 (block handle: offset 10, size 20)
	var handleBuf bytes.Buffer
	writeVarint64(&handleBuf, 10)
	writeVarint64(&handleBuf, 20)
	handleBytes := handleBuf.Bytes()

	writeVarint32(&buf, 0)
	writeVarint32(&buf, 4)
	writeVarint32(&buf, uint32(len(handleBytes)))
	buf.WriteString("key1")
	buf.Write(handleBytes)

	// Restarts
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(1))

	validData := buf.Bytes()
	handles, err := parseIndexBlock(validData)
	if err != nil {
		t.Fatalf("parseIndexBlock failed on valid data: %v", err)
	}
	if len(handles) != 1 || handles[0].Offset != 10 || handles[0].Size != 20 {
		t.Fatalf("unexpected handles: %v", handles)
	}

	// Truncate at restarts
	_, err = parseIndexBlock(validData[:len(validData)-2])
	if err == nil {
		t.Errorf("expected error for truncated index block at restarts")
	}

	// Truncate inside key/value
	_, err = parseIndexBlock(validData[:len(validData)-10])
	if err == nil {
		t.Errorf("expected error for truncated index block in entry")
	}
}

func TestReadRawBlockBoundsAndOverflow(t *testing.T) {
	fileData := make([]byte, 100)

	// 1. Offset near MaxUint64
	_, err := readRawBlock(fileData, BlockHandle{Offset: ^uint64(0), Size: 10})
	if err == nil {
		t.Errorf("expected error for offset near MaxUint64")
	}

	// 2. Size near MaxUint64 causing overflow if added
	_, err = readRawBlock(fileData, BlockHandle{Offset: 50, Size: ^uint64(0) - 10})
	if err == nil {
		t.Errorf("expected error for size overflowing uint64")
	}

	// 3. Offset + size exceeds file length
	_, err = readRawBlock(fileData, BlockHandle{Offset: 60, Size: 50})
	if err == nil {
		t.Errorf("expected error when offset+size > fileLen")
	}

	// 4. Offset + size leaves less than 5 bytes for trailer
	_, err = readRawBlock(fileData, BlockHandle{Offset: 0, Size: 97}) // leaves 3 bytes
	if err == nil {
		t.Errorf("expected error when trailer < 5 bytes")
	}
}

func TestReadRawBlockChecksum(t *testing.T) {
	payload := []byte("hello leveldb block")
	compressionNone := byte(0)

	calcCRC := crc32.Update(0, crc32cTable, payload)
	calcCRC = crc32.Update(calcCRC, crc32cTable, []byte{compressionNone})
	masked := MaskCRC(calcCRC)

	var validBlock bytes.Buffer
	validBlock.Write(payload)
	validBlock.WriteByte(compressionNone)
	_ = binary.Write(&validBlock, binary.LittleEndian, masked)

	// 1. Valid None block
	out, err := readRawBlock(validBlock.Bytes(), BlockHandle{Offset: 0, Size: uint64(len(payload))})
	if err != nil {
		t.Fatalf("readRawBlock failed on valid block: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Errorf("payload mismatch: %q vs %q", out, payload)
	}

	// 2. Corrupted CRC
	corruptBlock := append([]byte(nil), validBlock.Bytes()...)
	corruptBlock[len(corruptBlock)-1] ^= 0xff
	_, err = readRawBlock(corruptBlock, BlockHandle{Offset: 0, Size: uint64(len(payload))})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected checksum mismatch error, got: %v", err)
	}

	// 3. Valid Snappy compressed block
	var snappyBuf bytes.Buffer
	writeVarint32(&snappyBuf, uint32(len(payload)))
	snappyBuf.WriteByte(byte(len(payload)-1) << 2) // literal
	snappyBuf.Write(payload)
	snappyPayload := snappyBuf.Bytes()

	compressionSnappy := byte(1)
	calcCRCSnappy := crc32.Update(0, crc32cTable, snappyPayload)
	calcCRCSnappy = crc32.Update(calcCRCSnappy, crc32cTable, []byte{compressionSnappy})
	maskedSnappy := MaskCRC(calcCRCSnappy)

	var validSnappyBlock bytes.Buffer
	validSnappyBlock.Write(snappyPayload)
	validSnappyBlock.WriteByte(compressionSnappy)
	_ = binary.Write(&validSnappyBlock, binary.LittleEndian, maskedSnappy)

	outSnappy, err := readRawBlock(validSnappyBlock.Bytes(), BlockHandle{Offset: 0, Size: uint64(len(snappyPayload))})
	if err != nil {
		t.Fatalf("readRawBlock failed on valid Snappy block: %v", err)
	}
	if !bytes.Equal(outSnappy, payload) {
		t.Errorf("decompressed Snappy payload mismatch: %q vs %q", outSnappy, payload)
	}

	// 4. Corrupted Snappy block CRC
	corruptSnappy := append([]byte(nil), validSnappyBlock.Bytes()...)
	corruptSnappy[len(corruptSnappy)-1] ^= 0xaa
	_, err = readRawBlock(corruptSnappy, BlockHandle{Offset: 0, Size: uint64(len(snappyPayload))})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected checksum mismatch error on Snappy block, got: %v", err)
	}
}
