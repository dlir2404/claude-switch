package leveldb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type DBRecord struct {
	Key      []byte
	Value    []byte
	Seq      uint64
	IsDelete bool
}

// ReadConsistentDatabase reads all active SSTable (.ldb) files followed by active WAL (.log) files,
// filtering obsolete tables and logs using MANIFEST, and applying sequence ordering with tombstones.
func ReadConsistentDatabase(dbDir string) (map[string]DBRecord, error) {
	entries, err := os.ReadDir(dbDir)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("không thể đọc thư mục LevelDB: %w", err)
	}

	hasDataFiles := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".ldb") || strings.HasSuffix(name, ".sst") || strings.HasSuffix(name, ".log") {
			hasDataFiles = true
			break
		}
	}
	if !hasDataFiles {
		// Empty database directory (e.g. new profile before first launch)
		return make(map[string]DBRecord), nil
	}

	// Read active file numbers and current log number from MANIFEST
	manifestState, err := ReadManifest(dbDir)
	if err != nil {
		return nil, fmt.Errorf("không thể đọc MANIFEST LevelDB: %w", err)
	}

	var ldbFiles []string
	var logFiles []string

	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".ldb") || strings.HasSuffix(name, ".sst") {
			// Only read active tables recorded in MANIFEST
			if fnum, ok := ExtractFileNumber(name); ok {
				if !manifestState.ActiveTables[fnum] {
					continue // obsolete / unreferenced SSTable file
				}
			}
			ldbFiles = append(ldbFiles, filepath.Join(dbDir, name))
		} else if strings.HasSuffix(name, ".log") {
			// If MANIFEST specifies active logNumber, skip obsolete logs
			if manifestState.LogNumber > 0 {
				if fnum, ok := ExtractFileNumber(name); ok {
					if fnum < manifestState.LogNumber && fnum != manifestState.PrevLogNumber {
						continue // obsolete / compacted WAL log file
					}
				}
			}
			logFiles = append(logFiles, filepath.Join(dbDir, name))
		}
	}
	sort.Strings(ldbFiles)
	sort.Strings(logFiles)

	dbMap := make(map[string]DBRecord)

	// 1. Read compacted SSTable files
	for _, f := range ldbFiles {
		records, err := ReadTableFile(f)
		if err != nil {
			return nil, fmt.Errorf("lỗi khi đọc SSTable %s: %w", filepath.Base(f), err)
		}
		for _, r := range records {
			k := string(r.UserKey)
			existing, ok := dbMap[k]
			if !ok || r.Seq > existing.Seq {
				dbMap[k] = DBRecord{
					Key:      r.UserKey,
					Value:    r.Value,
					Seq:      r.Seq,
					IsDelete: r.IsDelete,
				}
			}
		}
	}

	// 2. Read active WAL .log files with sequence numbers and tombstones
	for _, f := range logFiles {
		walRecords, err := ReadWALRecords(f)
		if err != nil {
			return nil, fmt.Errorf("lỗi khi đọc WAL log %s: %w", filepath.Base(f), err)
		}
		for _, r := range walRecords {
			k := string(r.Key)
			existing, ok := dbMap[k]
			// Strictly newer sequence number wins (LSM tree semantics)
			if !ok || r.Seq > existing.Seq {
				dbMap[k] = DBRecord{
					Key:      r.Key,
					Value:    r.Value,
					Seq:      r.Seq,
					IsDelete: r.IsDelete,
				}
			}
		}
	}

	// Filter out deleted records
	activeRecords := make(map[string]DBRecord)
	for k, r := range dbMap {
		if !r.IsDelete && len(r.Value) > 0 {
			activeRecords[k] = r
		}
	}

	return activeRecords, nil
}
