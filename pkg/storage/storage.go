package storage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Cache directories known to be regenerable by the embedded Chromium/Electron app.
// Keep account state, IndexedDB, extension data, and conversation storage untouched.
var cacheDirectoryNames = map[string]bool{
	"Cache":             true,
	"Code Cache":        true,
	"GPUCache":          true,
	"GraphiteDawnCache": true,
	"DawnGraphiteCache": true,
	"DawnWebGPUCache":   true,
	"DawnCache":         true,
	"ShaderCache":       true,
	"GrShaderCache":     true,
}

type DirectorySize struct {
	Name  string
	Bytes int64
}

type ProfileSize struct {
	Name       string
	TotalBytes int64
	CacheBytes int64
	Largest    []DirectorySize
}

type Candidate struct {
	Profile string
	Name    string
	Path    string
	Bytes   int64
}

func Scan(profilesDir string) ([]ProfileSize, error) {
	entries, err := os.ReadDir(profilesDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var reports []ProfileSize
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		profilePath := filepath.Join(profilesDir, entry.Name())
		top, err := topLevelSizes(profilePath)
		if err != nil {
			return nil, fmt.Errorf("scan profile %q: %w", entry.Name(), err)
		}
		report := ProfileSize{Name: entry.Name()}
		for _, item := range top {
			report.TotalBytes += item.Bytes
			if cacheDirectoryNames[item.Name] {
				report.CacheBytes += item.Bytes
			}
		}
		sort.Slice(top, func(i, j int) bool { return top[i].Bytes > top[j].Bytes })
		if len(top) > 8 {
			top = top[:8]
		}
		report.Largest = top
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].TotalBytes > reports[j].TotalBytes })
	return reports, nil
}

func CleanCandidates(profilesDir string) []Candidate {
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return nil
	}
	var candidates []Candidate
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		profilePath := filepath.Join(profilesDir, entry.Name())
		for name := range cacheDirectoryNames {
			path := filepath.Join(profilePath, name)
			info, err := os.Lstat(path)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			bytes, err := treeSize(path)
			if err == nil && bytes > 0 {
				candidates = append(candidates, Candidate{Profile: entry.Name(), Name: name, Path: path, Bytes: bytes})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Bytes > candidates[j].Bytes })
	return candidates
}

func Clean(profilesDir string) ([]string, int64, error) {
	candidates := CleanCandidates(profilesDir)
	var removed []string
	var freed int64
	for _, item := range candidates {
		// Recheck the path immediately before deletion and never traverse a symlink.
		info, err := os.Lstat(item.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return removed, freed, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return removed, freed, fmt.Errorf("refusing to remove non-directory or symlink %q", item.Path)
		}
		if err := os.RemoveAll(item.Path); err != nil {
			return removed, freed, fmt.Errorf("remove %q: %w", item.Path, err)
		}
		removed = append(removed, item.Path)
		freed += item.Bytes
	}
	return removed, freed, nil
}

func TotalCandidateBytes(items []Candidate) int64 {
	var total int64
	for _, item := range items {
		total += item.Bytes
	}
	return total
}

func topLevelSizes(root string) ([]DirectorySize, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var result []DirectorySize
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		bytes, err := treeSize(path)
		if err == nil {
			result = append(result, DirectorySize{Name: entry.Name(), Bytes: bytes})
		}
	}
	return result, nil
}

func treeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
