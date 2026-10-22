package leveldb

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LevelDB VersionEdit tags (from LevelDB dbformat.h)
const (
	tagComparator     = 1
	tagLogNumber      = 2
	tagNextFileNumber = 3
	tagLastSequence   = 4
	tagCompactPointer = 5
	tagDeletedFile    = 6
	tagNewFile        = 7
	tagPrevLogNumber  = 9
)

type ManifestState struct {
	ActiveTables  map[uint64]bool
	LogNumber     uint64
	PrevLogNumber uint64
}

func discardBytes(r io.Reader, n int64) error {
	if n < 0 {
		return fmt.Errorf("invalid negative byte count: %d", n)
	}
	if n == 0 {
		return nil
	}
	written, err := io.CopyN(io.Discard, r, n)
	if err != nil || written != n {
		return fmt.Errorf("truncated data: expected %d bytes, got %d (err: %v)", n, written, err)
	}
	return nil
}

// ReadManifest parses the active MANIFEST file pointed to by CURRENT,
// properly skipping 32KB block padding, validating CRC32C, and reassembling multi-chunk records.
func ReadManifest(dbDir string) (*ManifestState, error) {
	currentPath := filepath.Join(dbDir, "CURRENT")
	currentData, err := os.ReadFile(currentPath)
	if err != nil {
		return nil, fmt.Errorf("không thể đọc file CURRENT: %w", err)
	}

	manifestName := strings.TrimSpace(string(currentData))
	if manifestName == "" {
		return nil, fmt.Errorf("file CURRENT rỗng")
	}

	manifestPath := filepath.Join(dbDir, manifestName)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("không thể đọc file MANIFEST %s: %w", manifestName, err)
	}

	// ReadLogPayloads handles 32KB boundaries, verifies CRCs, and reassembles fragments
	payloads, err := ReadLogPayloads(manifestData)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi đọc manifest log payloads: %w", err)
	}
	if len(payloads) == 0 {
		return nil, fmt.Errorf("file MANIFEST %s không chứa bản ghi hợp lệ", manifestName)
	}

	state := &ManifestState{
		ActiveTables: make(map[uint64]bool),
	}

	for _, payload := range payloads {
		stream := bytes.NewReader(payload)
		for stream.Len() > 0 {
			tag, err := ReadVarint32(stream)
			if err != nil {
				return nil, fmt.Errorf("corrupt tag in manifest: %w", err)
			}

			switch tag {
			case tagComparator:
				l, err := ReadVarint32(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt comparator length: %w", err)
				}
				if err := discardBytes(stream, int64(l)); err != nil {
					return nil, fmt.Errorf("truncated comparator: %w", err)
				}
			case tagLogNumber:
				num, err := readVarint64(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt log_number: %w", err)
				}
				state.LogNumber = num
			case tagPrevLogNumber:
				num, err := readVarint64(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt prev_log_number: %w", err)
				}
				state.PrevLogNumber = num
			case tagNextFileNumber, tagLastSequence:
				_, err = readVarint64(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt next/last sequence in manifest: %w", err)
				}
			case tagCompactPointer:
				if _, err := ReadVarint32(stream); err != nil {
					return nil, fmt.Errorf("corrupt compact_pointer level: %w", err)
				}
				l, err := ReadVarint32(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt compact_pointer key length: %w", err)
				}
				if err := discardBytes(stream, int64(l)); err != nil {
					return nil, fmt.Errorf("truncated compact_pointer key: %w", err)
				}
			case tagDeletedFile:
				if _, err := ReadVarint32(stream); err != nil {
					return nil, fmt.Errorf("corrupt deleted_file level: %w", err)
				}
				filenum, err := readVarint64(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt deleted_file filenum: %w", err)
				}
				delete(state.ActiveTables, filenum)
			case tagNewFile:
				if _, err := ReadVarint32(stream); err != nil {
					return nil, fmt.Errorf("corrupt new_file level: %w", err)
				}
				filenum, err := readVarint64(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt new_file filenum: %w", err)
				}
				if _, err := readVarint64(stream); err != nil {
					return nil, fmt.Errorf("corrupt new_file size: %w", err)
				}
				l1, err := ReadVarint32(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt new_file smallest_key length: %w", err)
				}
				if err := discardBytes(stream, int64(l1)); err != nil {
					return nil, fmt.Errorf("truncated new_file smallest_key: %w", err)
				}
				l2, err := ReadVarint32(stream)
				if err != nil {
					return nil, fmt.Errorf("corrupt new_file largest_key length: %w", err)
				}
				if err := discardBytes(stream, int64(l2)); err != nil {
					return nil, fmt.Errorf("truncated new_file largest_key: %w", err)
				}
				state.ActiveTables[filenum] = true
			default:
				return nil, fmt.Errorf("unknown tag %d in manifest payload", tag)
			}
		}
	}

	return state, nil
}

func ExtractFileNumber(filename string) (uint64, bool) {
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	numStr := strings.TrimSuffix(base, ext)
	num, err := strconv.ParseUint(numStr, 10, 64)
	if err != nil {
		return 0, false
	}
	return num, true
}
