package handoff

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinAndUnpinPreservesLatestSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotName := "2026-10-22_150000_test-task.md"
	snapshotPath := filepath.Join(tmpDir, snapshotName)
	if err := os.WriteFile(snapshotPath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to create snapshot: %v", err)
	}

	latestLink := filepath.Join(tmpDir, "latest.md")
	if err := os.Symlink(snapshotPath, latestLink); err != nil {
		t.Fatalf("failed to create latest symlink: %v", err)
	}

	// 1. Pin snapshot
	if err := PinHandoff(tmpDir, snapshotName); err != nil {
		t.Fatalf("PinHandoff failed: %v", err)
	}

	// Check if latest.md still resolves correctly
	target, err := os.Readlink(latestLink)
	if err != nil {
		t.Fatalf("latest.md broken after pin: %v", err)
	}
	pinnedName := "2026-10-22_150000_test-task.pinned.md"
	if filepath.Base(target) != pinnedName {
		t.Errorf("expected latest.md to point to %q, got %q", pinnedName, target)
	}
	if _, err := os.Stat(latestLink); err != nil {
		t.Errorf("latest.md does not exist on disk after pin: %v", err)
	}

	// 2. Unpin snapshot
	if err := UnpinHandoff(tmpDir, pinnedName); err != nil {
		t.Fatalf("UnpinHandoff failed: %v", err)
	}

	targetAfter, err := os.Readlink(latestLink)
	if err != nil {
		t.Fatalf("latest.md broken after unpin: %v", err)
	}
	if filepath.Base(targetAfter) != snapshotName {
		t.Errorf("expected latest.md to point to %q, got %q", snapshotName, targetAfter)
	}
	if _, err := os.Stat(latestLink); err != nil {
		t.Errorf("latest.md does not exist on disk after unpin: %v", err)
	}
}
