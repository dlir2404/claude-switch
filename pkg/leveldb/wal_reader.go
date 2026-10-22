package leveldb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

type KeyValue struct {
	Key   []byte
	Value []byte
}

type WALRecord struct {
	Key      []byte
	Value    []byte
	Seq      uint64
	IsDelete bool
}

const (
	recordTypeZero   = 0
	recordTypeFull   = 1
	recordTypeFirst  = 2
	recordTypeMiddle = 3
	recordTypeLast   = 4
)

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

func MaskCRC(crc uint32) uint32 {
	return ((crc >> 15) | (crc << 17)) + 0xa282ead8
}

func UnmaskCRC(masked uint32) uint32 {
	rot := masked - 0xa282ead8
	return ((rot >> 17) | (rot << 15))
}

// ReadLogPayloads extracts all logical records from LevelDB log/manifest data,
// handling 32KB block boundaries, zero-length padding, validating CRC32C checksums,
// and reassembling multi-chunk fragments.
func ReadLogPayloads(data []byte) ([][]byte, error) {
	var results [][]byte
	var assembledBuffer []byte
	pos := 0
	dataLen := len(data)

	for pos < dataLen {
		blockRemaining := 32768 - (pos % 32768)
		if blockRemaining < 7 {
			pos += blockRemaining
			continue
		}

		if pos+7 > dataLen {
			// Incomplete record header at EOF (crash during write); ignore
			break
		}

		storedCRC := binary.LittleEndian.Uint32(data[pos : pos+4])
		length := int(binary.LittleEndian.Uint16(data[pos+4 : pos+6]))
		recordType := data[pos+6]

		// Zero length & zero type represents trailer padding at block end
		if length == 0 && recordType == recordTypeZero {
			pos += blockRemaining
			continue
		}

		payloadStart := pos + 7
		payloadEnd := payloadStart + length
		if payloadEnd > dataLen {
			// Incomplete record payload at EOF (crash during write); ignore trailing incomplete chunk
			break
		}

		payload := data[payloadStart:payloadEnd]
		pos = payloadEnd

		// Verify CRC32C
		calcCRC := crc32.Update(0, crc32cTable, []byte{recordType})
		calcCRC = crc32.Update(calcCRC, crc32cTable, payload)
		if MaskCRC(calcCRC) != storedCRC {
			return nil, fmt.Errorf("leveldb log checksum mismatch at offset %d: expected 0x%08x, got 0x%08x", pos-length-7, storedCRC, MaskCRC(calcCRC))
		}

		switch recordType {
		case recordTypeFull:
			assembledBuffer = nil
			results = append(results, payload)
		case recordTypeFirst:
			assembledBuffer = append([]byte(nil), payload...)
		case recordTypeMiddle:
			if len(assembledBuffer) == 0 {
				return nil, errors.New("leveldb log: middle record type without preceding first record")
			}
			assembledBuffer = append(assembledBuffer, payload...)
		case recordTypeLast:
			if len(assembledBuffer) == 0 {
				return nil, errors.New("leveldb log: last record type without preceding first record")
			}
			assembledBuffer = append(assembledBuffer, payload...)
			results = append(results, assembledBuffer)
			assembledBuffer = nil
		default:
			return nil, fmt.Errorf("leveldb log: unexpected record type %d", recordType)
		}
	}

	return results, nil
}

func ReadWALPuts(logPath string) ([]KeyValue, error) {
	records, err := ReadWALRecords(logPath)
	if err != nil {
		return nil, err
	}
	var puts []KeyValue
	for _, r := range records {
		if !r.IsDelete {
			puts = append(puts, KeyValue{Key: r.Key, Value: r.Value})
		}
	}
	return puts, nil
}

func ReadWALRecords(logPath string) ([]WALRecord, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}

	payloads, err := ReadLogPayloads(data)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi đọc WAL log %s: %w", logPath, err)
	}

	var results []WALRecord
	for idx, p := range payloads {
		batchRecords, err := parseWriteBatch(p)
		if err != nil {
			return nil, fmt.Errorf("lỗi khi parse writebatch %d trong log %s: %w", idx, logPath, err)
		}
		results = append(results, batchRecords...)
	}

	return results, nil
}

func parseWriteBatch(payload []byte) ([]WALRecord, error) {
	if len(payload) < 12 {
		return nil, fmt.Errorf("writebatch payload quá ngắn: %d < 12", len(payload))
	}

	baseSeq := binary.LittleEndian.Uint64(payload[0:8])
	count := binary.LittleEndian.Uint32(payload[8:12])
	body := payload[12:]

	// Bounded preallocation: each entry takes at least 2 bytes (1 byte tag + 1 byte varint length)
	maxPossible := uint32(len(body) / 2)
	initialCap := count
	if initialCap > maxPossible {
		initialCap = maxPossible
	}
	if initialCap > 1024 {
		initialCap = 1024
	}
	results := make([]WALRecord, 0, initialCap)

	reader := bytes.NewReader(body)

	for i := uint32(0); i < count; i++ {
		currentSeq := baseSeq + uint64(i)
		tag, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("writebatch bị cắt trước entry %d (kỳ vọng %d entries): %w", i, count, err)
		}
		if tag == 0 { // Delete
			klen, err := ReadVarint32(reader)
			if err != nil {
				return nil, fmt.Errorf("lỗi đọc key length trong delete entry %d: %w", i, err)
			}
			if uint64(klen) > uint64(reader.Len()) {
				return nil, fmt.Errorf("key length %d vượt quá dữ liệu còn lại (%d bytes) trong delete entry %d", klen, reader.Len(), i)
			}
			key := make([]byte, klen)
			if _, err := io.ReadFull(reader, key); err != nil {
				return nil, fmt.Errorf("truncated key trong delete entry %d: %w", i, err)
			}
			results = append(results, WALRecord{
				Key:      key,
				Value:    nil,
				Seq:      currentSeq,
				IsDelete: true,
			})
		} else if tag == 1 { // Put
			klen, err := ReadVarint32(reader)
			if err != nil {
				return nil, fmt.Errorf("lỗi đọc key length trong put entry %d: %w", i, err)
			}
			if uint64(klen) > uint64(reader.Len()) {
				return nil, fmt.Errorf("key length %d vượt quá dữ liệu còn lại (%d bytes) trong put entry %d", klen, reader.Len(), i)
			}
			key := make([]byte, klen)
			if _, err := io.ReadFull(reader, key); err != nil {
				return nil, fmt.Errorf("truncated key trong put entry %d: %w", i, err)
			}

			vlen, err := ReadVarint32(reader)
			if err != nil {
				return nil, fmt.Errorf("lỗi đọc value length trong put entry %d: %w", i, err)
			}
			if uint64(vlen) > uint64(reader.Len()) {
				return nil, fmt.Errorf("value length %d vượt quá dữ liệu còn lại (%d bytes) trong put entry %d", vlen, reader.Len(), i)
			}
			val := make([]byte, vlen)
			if _, err := io.ReadFull(reader, val); err != nil {
				return nil, fmt.Errorf("truncated value trong put entry %d: %w", i, err)
			}

			results = append(results, WALRecord{
				Key:      key,
				Value:    val,
				Seq:      currentSeq,
				IsDelete: false,
			})
		} else {
			return nil, fmt.Errorf("writebatch chứa tag %d không hỗ trợ tại entry %d", tag, i)
		}
	}

	if reader.Len() > 0 {
		return nil, fmt.Errorf("writebatch còn thừa %d bytes chưa phân tích sau %d entries", reader.Len(), count)
	}

	return results, nil
}
