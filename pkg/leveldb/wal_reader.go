package leveldb

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
)

type KeyValue struct {
	Key   []byte
	Value []byte
}

const (
	recordTypeZero   = 0
	recordTypeFull   = 1
	recordTypeFirst  = 2
	recordTypeMiddle = 3
	recordTypeLast   = 4
)

func ReadWALPuts(logPath string) ([]KeyValue, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}

	var results []KeyValue
	var assembledBuffer []byte
	pos := 0
	dataLen := len(data)

	for pos+7 <= dataLen {
		blockRemaining := 32768 - (pos % 32768)
		if blockRemaining < 7 {
			pos += blockRemaining
			continue
		}

		length := int(binary.LittleEndian.Uint16(data[pos+4 : pos+6]))
		recordType := data[pos+6]

		if length == 0 {
			pos += blockRemaining
			continue
		}

		payloadStart := pos + 7
		payloadEnd := payloadStart + length
		if payloadEnd > dataLen {
			break
		}
		payload := data[payloadStart:payloadEnd]
		pos = payloadEnd

		switch recordType {
		case recordTypeFull:
			assembledBuffer = nil
			results = append(results, parseWriteBatch(payload)...)
		case recordTypeFirst:
			assembledBuffer = append([]byte(nil), payload...)
		case recordTypeMiddle:
			if len(assembledBuffer) > 0 {
				assembledBuffer = append(assembledBuffer, payload...)
			}
		case recordTypeLast:
			if len(assembledBuffer) > 0 {
				assembledBuffer = append(assembledBuffer, payload...)
				results = append(results, parseWriteBatch(assembledBuffer)...)
				assembledBuffer = nil
			}
		default:
			assembledBuffer = nil
		}
	}

	return results, nil
}

func parseWriteBatch(payload []byte) []KeyValue {
	if len(payload) <= 12 {
		return nil
	}

	var results []KeyValue
	count := binary.LittleEndian.Uint32(payload[8:12])
	reader := bytes.NewReader(payload[12:])

	for i := uint32(0); i < count; i++ {
		tag, err := reader.ReadByte()
		if err != nil {
			break
		}
		if tag == 0 { // Delete
			klen, err := ReadVarint32(reader)
			if err != nil {
				break
			}
			_, _ = reader.Seek(int64(klen), io.SeekCurrent)
		} else if tag == 1 { // Put
			klen, err := ReadVarint32(reader)
			if err != nil {
				break
			}
			key := make([]byte, klen)
			if _, err := io.ReadFull(reader, key); err != nil {
				break
			}

			vlen, err := ReadVarint32(reader)
			if err != nil {
				break
			}
			val := make([]byte, vlen)
			if _, err := io.ReadFull(reader, val); err != nil {
				break
			}

			results = append(results, KeyValue{
				Key:   key,
				Value: val,
			})
		}
	}

	return results
}
