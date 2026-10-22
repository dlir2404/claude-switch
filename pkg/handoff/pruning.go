package handoff

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"claude-switch/pkg/config"
)

type HandoffItem struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	IsLatest  bool      `json:"is_latest"`
	IsPinned  bool      `json:"is_pinned"`
}

func ListHandoffs(handoffsDir string) ([]HandoffItem, error) {
	entries, err := os.ReadDir(handoffsDir)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	latestTarget := ""
	latestLink := filepath.Join(handoffsDir, "latest.md")
	if target, err := os.Readlink(latestLink); err == nil {
		latestTarget = filepath.Base(target)
	}

	var items []HandoffItem
	for _, entry := range entries {
		name := entry.Name()
		if name == "latest.md" || !strings.HasSuffix(name, ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}

		item := HandoffItem{
			Name:     name,
			Path:     filepath.Join(handoffsDir, name),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			IsLatest: name == latestTarget,
			IsPinned: strings.Contains(name, ".pinned."),
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ModTime.After(items[j].ModTime)
	})

	return items, nil
}

func PruneHandoffs(handoffsDir string, cfg *config.PruningConfig, dryRun bool) ([]string, error) {
	if cfg != nil && !cfg.Enabled {
		return nil, nil
	}

	items, err := ListHandoffs(handoffsDir)
	if err != nil {
		return nil, err
	}

	var candidates []HandoffItem
	for _, it := range items {
		if cfg != nil && cfg.ProtectLatest && it.IsLatest {
			continue
		}
		if it.IsPinned {
			continue
		}
		candidates = append(candidates, it)
	}

	// Determine files to delete
	toDelete := make(map[string]bool)
	now := time.Now()

	// 1. By retention days
	if cfg != nil && cfg.RetentionDays > 0 {
		cutoff := now.Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
		for _, it := range candidates {
			if it.ModTime.Before(cutoff) {
				toDelete[it.Path] = true
			}
		}
	}

	// 2. By max snapshots
	maxSnap := 20
	if cfg != nil && cfg.MaxSnapshots > 0 {
		maxSnap = cfg.MaxSnapshots
	}
	remaining := 0
	for _, it := range candidates {
		if !toDelete[it.Path] {
			remaining++
			if remaining > maxSnap {
				toDelete[it.Path] = true
			}
		}
	}

	// 3. By total size
	maxSize := int64(50 * 1024 * 1024)
	if cfg != nil && cfg.MaxTotalSizeMB > 0 {
		maxSize = int64(cfg.MaxTotalSizeMB) * 1024 * 1024
	}
	var totalSize int64
	for _, it := range candidates {
		if !toDelete[it.Path] {
			totalSize += it.Size
			if totalSize > maxSize {
				toDelete[it.Path] = true
			}
		}
	}

	var deleted []string
	for path := range toDelete {
		deleted = append(deleted, path)
		if !dryRun {
			_ = os.Remove(path)
		}
	}

	return deleted, nil
}

func PinHandoff(handoffsDir, filename string) error {
	base := filepath.Base(filename)
	if strings.Contains(base, ".pinned.") {
		return nil // already pinned
	}
	oldPath := filepath.Join(handoffsDir, base)
	newBase := strings.TrimSuffix(base, ".md") + ".pinned.md"
	newPath := filepath.Join(handoffsDir, newBase)

	latestLink := filepath.Join(handoffsDir, "latest.md")
	isLatest := false
	if target, err := os.Readlink(latestLink); err == nil {
		if filepath.Base(target) == base {
			isLatest = true
		}
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}

	if isLatest {
		_ = os.Remove(latestLink)
		_ = os.Symlink(newPath, latestLink)
	}

	return nil
}

func UnpinHandoff(handoffsDir, filename string) error {
	base := filepath.Base(filename)
	if !strings.Contains(base, ".pinned.") {
		return nil
	}
	oldPath := filepath.Join(handoffsDir, base)
	newBase := strings.Replace(base, ".pinned.", ".", 1)
	newPath := filepath.Join(handoffsDir, newBase)

	latestLink := filepath.Join(handoffsDir, "latest.md")
	isLatest := false
	if target, err := os.Readlink(latestLink); err == nil {
		if filepath.Base(target) == base {
			isLatest = true
		}
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}

	if isLatest {
		_ = os.Remove(latestLink)
		_ = os.Symlink(newPath, latestLink)
	}

	return nil
}
