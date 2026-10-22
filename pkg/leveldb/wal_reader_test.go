package leveldb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
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
	// checksum 4 bytes, length 2 bytes, type 1 byte
	_ = binary.Write(fileBuf, binary.LittleEndian, uint32(0))
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
