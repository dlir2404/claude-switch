package leveldb

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeVarint32(buf *bytes.Buffer, v uint32) {
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

func buildWriteBatch(kvs []KeyValue) []byte {
	buf := new(bytes.Buffer)
	// seq (8 bytes)
	_ = binary.Write(buf, binary.LittleEndian, uint64(100))
	// count (4 bytes)
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(kvs)))

	for _, kv := range kvs {
		buf.WriteByte(1) // Put
		writeVarint32(buf, uint32(len(kv.Key)))
		buf.Write(kv.Key)
		writeVarint32(buf, uint32(len(kv.Value)))
		buf.Write(kv.Value)
	}
	return buf.Bytes()
}

func writeChunk(fileBuf *bytes.Buffer, recordType byte, payload []byte) {
	calcCRC := crc32.Update(0, crc32cTable, []byte{recordType})
	calcCRC = crc32.Update(calcCRC, crc32cTable, payload)
	masked := MaskCRC(calcCRC)

	_ = binary.Write(fileBuf, binary.LittleEndian, masked)
	_ = binary.Write(fileBuf, binary.LittleEndian, uint16(len(payload)))
	fileBuf.WriteByte(recordType)
	fileBuf.Write(payload)
}

func TestFragmentedWALReader(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	// Create a big value that will be fragmented across chunks
	bigVal := bytes.Repeat([]byte("A"), 40000)
	expectedKVs := []KeyValue{
		{Key: []byte("key1"), Value: []byte("val1")},
		{Key: []byte("big_key"), Value: bigVal},
	}

	batch := buildWriteBatch(expectedKVs)
	if len(batch) <= 30000 {
		t.Fatalf("batch too small for fragment test: %d", len(batch))
	}

	// Split batch into 3 fragments: FIRST, MIDDLE, LAST
	part1 := batch[:15000]
	part2 := batch[15000:30000]
	part3 := batch[30000:]

	fileBuf := new(bytes.Buffer)
	// 1. A FULL record first
	smallBatch := buildWriteBatch([]KeyValue{{Key: []byte("initial"), Value: []byte("data")}})
	writeChunk(fileBuf, recordTypeFull, smallBatch)

	// 2. Fragmented record (FIRST, MIDDLE, LAST)
	writeChunk(fileBuf, recordTypeFirst, part1)
	writeChunk(fileBuf, recordTypeMiddle, part2)
	writeChunk(fileBuf, recordTypeLast, part3)

	// 3. Another FULL record after
	finalBatch := buildWriteBatch([]KeyValue{{Key: []byte("trailing"), Value: []byte("record")}})
	writeChunk(fileBuf, recordTypeFull, finalBatch)

	if err := os.WriteFile(logPath, fileBuf.Bytes(), 0644); err != nil {
		t.Fatalf("failed to write test log: %v", err)
	}

	results, err := ReadWALPuts(logPath)
	if err != nil {
		t.Fatalf("ReadWALPuts failed: %v", err)
	}

	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	if string(results[0].Key) != "initial" || string(results[0].Value) != "data" {
		t.Errorf("result[0] mismatch: %v", results[0])
	}
	if string(results[1].Key) != "key1" || string(results[1].Value) != "val1" {
		t.Errorf("result[1] mismatch: %v", results[1])
	}
	if string(results[2].Key) != "big_key" || !bytes.Equal(results[2].Value, bigVal) {
		t.Errorf("result[2] (bigVal) mismatch: len got %d, want %d", len(results[2].Value), len(bigVal))
	}
	if string(results[3].Key) != "trailing" || string(results[3].Value) != "record" {
		t.Errorf("result[3] mismatch: %v", results[3])
	}
}

func TestWALChecksumMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "corrupt.log")

	fileBuf := new(bytes.Buffer)
	payload := []byte("valid payload")
	writeChunk(fileBuf, recordTypeFull, payload)

	// Corrupt payload byte
	raw := fileBuf.Bytes()
	raw[len(raw)-1] ^= 0xff

	if err := os.WriteFile(logPath, raw, 0644); err != nil {
		t.Fatalf("failed to write corrupt log: %v", err)
	}

	_, err := ReadWALRecords(logPath)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected checksum mismatch error, got: %v", err)
	}
}

func TestWALTruncatedRecordAtEOF(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "truncated.log")

	fileBuf := new(bytes.Buffer)
	smallBatch := buildWriteBatch([]KeyValue{{Key: []byte("k1"), Value: []byte("v1")}})
	writeChunk(fileBuf, recordTypeFull, smallBatch)

	raw := fileBuf.Bytes()
	// Append 4 bytes of an incomplete header (truncated crash at EOF)
	truncated := append(raw, 0x12, 0x34, 0x56, 0x78)

	if err := os.WriteFile(logPath, truncated, 0644); err != nil {
		t.Fatalf("failed to write truncated log: %v", err)
	}

	// Should successfully read complete record and cleanly ignore incomplete EOF record
	results, err := ReadWALPuts(logPath)
	if err != nil {
		t.Fatalf("ReadWALPuts failed: %v", err)
	}
	if len(results) != 1 || string(results[0].Key) != "k1" {
		t.Errorf("unexpected results: %v", results)
	}
}

func TestParseWriteBatchErrors(t *testing.T) {
	// 1. Count larger than actual entries
	var buf1 bytes.Buffer
	_ = binary.Write(&buf1, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf1, binary.LittleEndian, uint32(5)) // count = 5
	// only write 1 entry
	buf1.WriteByte(1) // Put
	writeVarint32(&buf1, 2)
	buf1.WriteString("k1")
	writeVarint32(&buf1, 2)
	buf1.WriteString("v1")

	_, err := parseWriteBatch(buf1.Bytes())
	if err == nil || !strings.Contains(err.Error(), "writebatch bị cắt") {
		t.Errorf("expected error for batch with count > actual entries, got: %v", err)
	}

	// 2. Truncated key (length exceeds remaining bytes)
	var buf2 bytes.Buffer
	_ = binary.Write(&buf2, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf2, binary.LittleEndian, uint32(1))
	buf2.WriteByte(1) // Put
	writeVarint32(&buf2, 100) // key len 100
	buf2.WriteString("short") // only 5 bytes

	_, err = parseWriteBatch(buf2.Bytes())
	if err == nil || (!strings.Contains(err.Error(), "truncated key") && !strings.Contains(err.Error(), "vượt quá dữ liệu còn lại")) {
		t.Errorf("expected error for truncated key, got: %v", err)
	}

	// 3. Truncated value (length exceeds remaining bytes)
	var buf3 bytes.Buffer
	_ = binary.Write(&buf3, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf3, binary.LittleEndian, uint32(1))
	buf3.WriteByte(1) // Put
	writeVarint32(&buf3, 2)
	buf3.WriteString("k1")
	writeVarint32(&buf3, 50) // value len 50
	buf3.WriteString("short_val")

	_, err = parseWriteBatch(buf3.Bytes())
	if err == nil || (!strings.Contains(err.Error(), "truncated value") && !strings.Contains(err.Error(), "vượt quá dữ liệu còn lại")) {
		t.Errorf("expected error for truncated value, got: %v", err)
	}

	// 4. Unsupported tag
	var buf4 bytes.Buffer
	_ = binary.Write(&buf4, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf4, binary.LittleEndian, uint32(1))
	buf4.WriteByte(99) // unsupported tag 99

	_, err = parseWriteBatch(buf4.Bytes())
	if err == nil || !strings.Contains(err.Error(), "tag 99 không hỗ trợ") {
		t.Errorf("expected error for unsupported tag, got: %v", err)
	}

	// 5. Trailing unparsed bytes
	var buf5 bytes.Buffer
	_ = binary.Write(&buf5, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf5, binary.LittleEndian, uint32(1))
	buf5.WriteByte(1) // Put
	writeVarint32(&buf5, 2)
	buf5.WriteString("k1")
	writeVarint32(&buf5, 2)
	buf5.WriteString("v1")
	buf5.WriteString("extra_trailing_bytes")

	_, err = parseWriteBatch(buf5.Bytes())
	if err == nil || !strings.Contains(err.Error(), "còn thừa") {
		t.Errorf("expected error for trailing bytes, got: %v", err)
	}

	// 6. Very short batch declaring enormous count (billion entries)
	var buf6 bytes.Buffer
	_ = binary.Write(&buf6, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf6, binary.LittleEndian, uint32(1_000_000_000)) // 1 billion
	// Put 1 entry and then cut off
	buf6.WriteByte(0) // Delete tag
	writeVarint32(&buf6, 2)
	buf6.WriteString("k1")
	// No more entries
	_, err = parseWriteBatch(buf6.Bytes())
	if err == nil || !strings.Contains(err.Error(), "writebatch bị cắt") {
		t.Errorf("expected error for short batch with enormous count, got: %v", err)
	}

	// 7. Enormous key length
	var buf7 bytes.Buffer
	_ = binary.Write(&buf7, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf7, binary.LittleEndian, uint32(1))
	buf7.WriteByte(1) // Put
	writeVarint32(&buf7, 2_000_000_000) // 2 GB key length
	buf7.WriteString("abc")
	_, err = parseWriteBatch(buf7.Bytes())
	if err == nil || !strings.Contains(err.Error(), "vượt quá dữ liệu còn lại") {
		t.Errorf("expected error for huge key length, got: %v", err)
	}

	// 8. Enormous value length
	var buf8 bytes.Buffer
	_ = binary.Write(&buf8, binary.LittleEndian, uint64(1))
	_ = binary.Write(&buf8, binary.LittleEndian, uint32(1))
	buf8.WriteByte(1) // Put
	writeVarint32(&buf8, 2)
	buf8.WriteString("k1")
	writeVarint32(&buf8, 2_000_000_000) // 2 GB value length
	buf8.WriteString("abc")
	_, err = parseWriteBatch(buf8.Bytes())
	if err == nil || !strings.Contains(err.Error(), "vượt quá dữ liệu còn lại") {
		t.Errorf("expected error for huge value length, got: %v", err)
	}
}
