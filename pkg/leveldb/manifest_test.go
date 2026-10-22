package leveldb

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadManifestState(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Write CURRENT pointing to MANIFEST-000001
	currentPath := filepath.Join(tmpDir, "CURRENT")
	_ = os.WriteFile(currentPath, []byte("MANIFEST-000001\n"), 0644)

	// 2. Build MANIFEST log with new file 5, new file 6, deleted file 5, log_number 4
	var manifestBuf bytes.Buffer

	// Record 1: log number 4, new_file num=5 (tag=7)
	var rec1Payload bytes.Buffer
	rec1Payload.WriteByte(tagLogNumber)
	writeVarint32(&rec1Payload, 4) // log number = 4
	rec1Payload.WriteByte(tagNewFile)
	rec1Payload.WriteByte(0) // level 0
	writeVarint32(&rec1Payload, 5) // file num 5
	writeVarint32(&rec1Payload, 1024) // size 1024
	writeVarint32(&rec1Payload, 1) // key1 len
	rec1Payload.WriteByte('a')
	writeVarint32(&rec1Payload, 1) // key2 len
	rec1Payload.WriteByte('z')

	writeChunk(&manifestBuf, recordTypeFull, rec1Payload.Bytes())

	// Pad until block boundary (32768)
	rem := 32768 - (manifestBuf.Len() % 32768)
	manifestBuf.Write(make([]byte, rem))

	// Record 2 in next block: add file 6, delete file 5
	var rec2Payload bytes.Buffer
	rec2Payload.WriteByte(tagNewFile)
	rec2Payload.WriteByte(0)
	writeVarint32(&rec2Payload, 6)
	writeVarint32(&rec2Payload, 2048)
	writeVarint32(&rec2Payload, 1)
	rec2Payload.WriteByte('a')
	writeVarint32(&rec2Payload, 1)
	rec2Payload.WriteByte('z')

	rec2Payload.WriteByte(tagDeletedFile)
	rec2Payload.WriteByte(0)
	writeVarint32(&rec2Payload, 5) // delete file 5

	writeChunk(&manifestBuf, recordTypeFull, rec2Payload.Bytes())

	manifestPath := filepath.Join(tmpDir, "MANIFEST-000001")
	_ = os.WriteFile(manifestPath, manifestBuf.Bytes(), 0644)

	state, err := ReadManifest(tmpDir)
	if err != nil {
		t.Fatalf("ReadManifest failed: %v", err)
	}

	if state.LogNumber != 4 {
		t.Errorf("expected LogNumber 4, got %d", state.LogNumber)
	}
	if state.ActiveTables[5] {
		t.Errorf("file 5 was deleted by compaction, should not be active")
	}
	if !state.ActiveTables[6] {
		t.Errorf("file 6 should be active")
	}
}

func TestReadManifestErrors(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Missing CURRENT
	_, err := ReadManifest(tmpDir)
	if err == nil {
		t.Errorf("expected error when CURRENT is missing, got nil")
	}

	// 2. Missing MANIFEST referenced by CURRENT
	currentPath := filepath.Join(tmpDir, "CURRENT")
	_ = os.WriteFile(currentPath, []byte("MANIFEST-000099\n"), 0644)
	_, err = ReadManifest(tmpDir)
	if err == nil || !strings.Contains(err.Error(), "MANIFEST-000099") {
		t.Errorf("expected error referencing missing manifest, got: %v", err)
	}

	// 3. Corrupt MANIFEST with unknown tag
	manifestPath := filepath.Join(tmpDir, "MANIFEST-000099")
	var corruptBuf bytes.Buffer
	var payload bytes.Buffer
	writeVarint32(&payload, 255) // invalid unknown tag
	writeChunk(&corruptBuf, recordTypeFull, payload.Bytes())
	_ = os.WriteFile(manifestPath, corruptBuf.Bytes(), 0644)

	_, err = ReadManifest(tmpDir)
	if err == nil || !strings.Contains(err.Error(), "unknown tag") {
		t.Errorf("expected error for corrupt manifest with unknown tag, got: %v", err)
	}

	// 4. Corrupt MANIFEST with field length exceeding payload
	var truncatedFieldBuf bytes.Buffer
	var truncPayload bytes.Buffer
	truncPayload.WriteByte(tagComparator)
	writeVarint32(&truncPayload, 100) // declared len 100
	truncPayload.WriteString("short") // only 5 bytes
	writeChunk(&truncatedFieldBuf, recordTypeFull, truncPayload.Bytes())
	_ = os.WriteFile(manifestPath, truncatedFieldBuf.Bytes(), 0644)

	_, err = ReadManifest(tmpDir)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Errorf("expected error for truncated comparator field, got: %v", err)
	}

	// 5. Corrupt MANIFEST with bad CRC
	raw := truncatedFieldBuf.Bytes()
	raw[len(raw)-1] ^= 0x55 // corrupt byte
	_ = os.WriteFile(manifestPath, raw, 0644)

	_, err = ReadManifest(tmpDir)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected checksum mismatch error on corrupt manifest, got: %v", err)
	}
}
