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

const TableMagic = uint64(0xdb4775248b80fb57)

type TableEntry struct {
	UserKey  []byte
	Value    []byte
	Seq      uint64
	IsDelete bool
}

type BlockHandle struct {
	Offset uint64
	Size   uint64
}

func readBlockHandle(r io.ByteReader) (BlockHandle, error) {
	offset, err := readVarint64(r)
	if err != nil {
		return BlockHandle{}, err
	}
	size, err := readVarint64(r)
	if err != nil {
		return BlockHandle{}, err
	}
	return BlockHandle{Offset: offset, Size: size}, nil
}

func ReadTableFile(filePath string) ([]TableEntry, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	if len(data) < 48 {
		return nil, errors.New("table file too short for footer")
	}

	footer := data[len(data)-48:]
	magic := binary.LittleEndian.Uint64(footer[40:48])
	if magic != TableMagic {
		return nil, errors.New("invalid table magic number")
	}

	footerReader := bytes.NewReader(footer)
	// metaindex handle
	_, err = readBlockHandle(footerReader)
	if err != nil {
		return nil, err
	}
	// index handle
	indexHandle, err := readBlockHandle(footerReader)
	if err != nil {
		return nil, err
	}

	// Read Index Block
	indexBlockData, err := readRawBlock(data, indexHandle)
	if err != nil {
		return nil, fmt.Errorf("failed to read index block: %w", err)
	}

	// Parse Index Block to find all Data Block handles
	dataHandles, err := parseIndexBlock(indexBlockData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse index block: %w", err)
	}

	var allEntries []TableEntry
	for _, handle := range dataHandles {
		blockData, err := readRawBlock(data, handle)
		if err != nil {
			return nil, fmt.Errorf("failed to read data block at offset %d: %w", handle.Offset, err)
		}
		entries, err := parseDataBlock(blockData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse data block at offset %d: %w", handle.Offset, err)
		}
		allEntries = append(allEntries, entries...)
	}

	return allEntries, nil
}

func readRawBlock(fileData []byte, handle BlockHandle) ([]byte, error) {
	fileLen := uint64(len(fileData))
	// Safe bounds checks without pre-addition to prevent integer overflow
	if handle.Offset > fileLen {
		return nil, fmt.Errorf("block offset %d out of bounds (file size %d)", handle.Offset, fileLen)
	}
	if handle.Size > fileLen-handle.Offset {
		return nil, fmt.Errorf("block size %d out of bounds at offset %d (file size %d)", handle.Size, handle.Offset, fileLen)
	}
	if fileLen-handle.Offset-handle.Size < 5 {
		return nil, fmt.Errorf("missing 5-byte trailer for block at offset %d, size %d (file size %d)", handle.Offset, handle.Size, fileLen)
	}

	start := int(handle.Offset)
	size := int(handle.Size)
	end := start + size

	blockPayload := fileData[start:end]
	compressionType := fileData[end]
	storedCRC := binary.LittleEndian.Uint32(fileData[end+1 : end+5])

	// Validate Block CRC32C (payload + compression byte)
	calcCRC := crc32.Update(0, crc32cTable, blockPayload)
	calcCRC = crc32.Update(calcCRC, crc32cTable, []byte{compressionType})
	if MaskCRC(calcCRC) != storedCRC {
		return nil, fmt.Errorf("block checksum mismatch at offset %d: expected 0x%08x, got 0x%08x", start, storedCRC, MaskCRC(calcCRC))
	}

	if compressionType == 0 { // None
		return blockPayload, nil
	} else if compressionType == 1 { // Snappy
		return DecompressSnappy(blockPayload)
	}

	return nil, fmt.Errorf("unsupported compression type: %d", compressionType)
}

func parseIndexBlock(data []byte) ([]BlockHandle, error) {
	if len(data) < 4 {
		return nil, errors.New("index block too short")
	}

	numRestarts := int(binary.LittleEndian.Uint32(data[len(data)-4:]))
	if numRestarts < 1 {
		return nil, errors.New("index block has no restarts (numRestarts < 1)")
	}
	restartsSize := numRestarts * 4
	restartsOffset := len(data) - 4 - restartsSize
	if restartsOffset < 0 {
		return nil, fmt.Errorf("corrupt restarts in index block: restartsSize %d > dataLen %d", restartsSize+4, len(data))
	}

	// Validate restart offsets
	for i := 0; i < numRestarts; i++ {
		offset := int(binary.LittleEndian.Uint32(data[restartsOffset+i*4 : restartsOffset+(i+1)*4]))
		if offset > restartsOffset {
			return nil, fmt.Errorf("corrupt restart offset %d > restartsOffset %d in index block", offset, restartsOffset)
		}
	}

	var handles []BlockHandle
	reader := bytes.NewReader(data[:restartsOffset])
	var prevKey []byte

	for reader.Len() > 0 {
		sharedLen, err := ReadVarint32(reader)
		if err != nil {
			return nil, fmt.Errorf("corrupt sharedLen in index block: %w", err)
		}
		unsharedLen, err := ReadVarint32(reader)
		if err != nil {
			return nil, fmt.Errorf("corrupt unsharedLen in index block: %w", err)
		}
		valLen, err := ReadVarint32(reader)
		if err != nil {
			return nil, fmt.Errorf("corrupt valLen in index block: %w", err)
		}

		if int(sharedLen) > len(prevKey) {
			return nil, fmt.Errorf("corrupt index block: sharedLen %d > len(prevKey) %d", sharedLen, len(prevKey))
		}

		keyDelta := make([]byte, unsharedLen)
		if _, err := io.ReadFull(reader, keyDelta); err != nil {
			return nil, fmt.Errorf("truncated key delta in index block: %w", err)
		}

		key := make([]byte, int(sharedLen)+int(unsharedLen))
		copy(key, prevKey[:sharedLen])
		copy(key[sharedLen:], keyDelta)
		prevKey = key

		val := make([]byte, valLen)
		if _, err := io.ReadFull(reader, val); err != nil {
			return nil, fmt.Errorf("truncated value in index block: %w", err)
		}

		handle, err := readBlockHandle(bytes.NewReader(val))
		if err != nil {
			return nil, fmt.Errorf("corrupt block handle in index: %w", err)
		}
		handles = append(handles, handle)
	}

	return handles, nil
}

func parseDataBlock(data []byte) ([]TableEntry, error) {
	if len(data) < 4 {
		return nil, errors.New("data block too short")
	}

	numRestarts := int(binary.LittleEndian.Uint32(data[len(data)-4:]))
	if numRestarts < 1 {
		return nil, errors.New("data block has no restarts (numRestarts < 1)")
	}
	restartsSize := numRestarts * 4
	restartsOffset := len(data) - 4 - restartsSize
	if restartsOffset < 0 {
		return nil, fmt.Errorf("corrupt restarts in data block: restartsSize %d > dataLen %d", restartsSize+4, len(data))
	}

	// Validate restart offsets
	for i := 0; i < numRestarts; i++ {
		offset := int(binary.LittleEndian.Uint32(data[restartsOffset+i*4 : restartsOffset+(i+1)*4]))
		if offset > restartsOffset {
			return nil, fmt.Errorf("corrupt restart offset %d > restartsOffset %d in data block", offset, restartsOffset)
		}
	}

	var entries []TableEntry
	reader := bytes.NewReader(data[:restartsOffset])
	var prevKey []byte

	for reader.Len() > 0 {
		sharedLen, err := ReadVarint32(reader)
		if err != nil {
			return nil, fmt.Errorf("corrupt sharedLen in data block: %w", err)
		}
		unsharedLen, err := ReadVarint32(reader)
		if err != nil {
			return nil, fmt.Errorf("corrupt unsharedLen in data block: %w", err)
		}
		valLen, err := ReadVarint32(reader)
		if err != nil {
			return nil, fmt.Errorf("corrupt valLen in data block: %w", err)
		}

		if int(sharedLen) > len(prevKey) {
			return nil, fmt.Errorf("corrupt data block: sharedLen %d > len(prevKey) %d", sharedLen, len(prevKey))
		}

		keyDelta := make([]byte, unsharedLen)
		if _, err := io.ReadFull(reader, keyDelta); err != nil {
			return nil, fmt.Errorf("truncated key delta in data block: %w", err)
		}

		key := make([]byte, int(sharedLen)+int(unsharedLen))
		copy(key, prevKey[:sharedLen])
		copy(key[sharedLen:], keyDelta)
		prevKey = key

		val := make([]byte, valLen)
		if _, err := io.ReadFull(reader, val); err != nil {
			return nil, fmt.Errorf("truncated value in data block: %w", err)
		}

		if len(key) < 8 {
			return nil, fmt.Errorf("corrupt internal key in data block: len %d < 8", len(key))
		}

		tag := binary.LittleEndian.Uint64(key[len(key)-8:])
		userKey := key[:len(key)-8]
		valType := byte(tag & 0xff)
		seq := tag >> 8

		entries = append(entries, TableEntry{
			UserKey:  userKey,
			Value:    val,
			Seq:      seq,
			IsDelete: valType == 0,
		})
	}

	return entries, nil
}
