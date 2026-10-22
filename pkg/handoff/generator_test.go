package handoff

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"claude-switch/pkg/config"
	"claude-switch/pkg/extractor"
)

func TestMaxRecentMessages(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Handoff.MaxRecentMessages = 3

	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			Title: "Test Conversation",
		},
	}
	for i := 1; i <= 10; i++ {
		detail.Messages = append(detail.Messages, extractor.Message{
			Sender: "human",
			Text:   fmt.Sprintf("Message number %d", i),
		})
	}

	prompt := buildLLMPrompt(cfg, detail, "test@example.com")

	// Must NOT contain Message number 1 to 7
	if strings.Contains(prompt, "Message number 1\n") || strings.Contains(prompt, "Message number 7\n") {
		t.Errorf("prompt contains older messages when MaxRecentMessages=3")
	}

	// MUST contain Message number 8, 9, 10
	if !strings.Contains(prompt, "Message number 8") || !strings.Contains(prompt, "Message number 10") {
		t.Errorf("prompt missing recent messages 8, 9, 10")
	}
}

func TestIncludeArtifactsFlag(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Handoff.IncludeArtifacts = false

	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			Title: "Test Artifacts Omission",
		},
		Artifacts: []extractor.Artifact{
			{
				Identifier: "secret-code",
				Title:      "Secret Component",
				Language:   "typescript",
				Content:    "const secret = 'do_not_leak';",
			},
		},
	}

	// 1. In LLM prompt
	prompt := buildLLMPrompt(cfg, detail, "test@example.com")
	if strings.Contains(prompt, "secret-code") || strings.Contains(prompt, "do_not_leak") {
		t.Errorf("LLM prompt contains artifact when IncludeArtifacts=false")
	}

	// 2. In Rule-based markdown
	md := GenerateRuleBasedMarkdown(cfg, detail, "test@example.com")
	if strings.Contains(md, "secret-code") || strings.Contains(md, "do_not_leak") {
		t.Errorf("Markdown contains artifact when IncludeArtifacts=false")
	}
}

func TestTightenHandoffPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	// Create with open permissions
	_ = os.Chmod(tmpDir, 0755)
	legacySnapshot := filepath.Join(tmpDir, "2026-01-01_120000_legacy.md")
	_ = os.WriteFile(legacySnapshot, []byte("legacy"), 0644)
	_ = os.Chmod(legacySnapshot, 0644)

	// An unrelated user document in the same directory
	unrelatedDoc := filepath.Join(tmpDir, "README.md")
	_ = os.WriteFile(unrelatedDoc, []byte("# My Readme"), 0644)
	_ = os.Chmod(unrelatedDoc, 0644)

	tightenHandoffPermissions(tmpDir)

	// 1. Since tmpDir is a custom directory (not ~/.claude-switch/handoffs), its permissions must NOT be forced to 0700
	dirInfo, err := os.Stat(tmpDir)
	if err != nil {
		t.Fatalf("failed to stat tmpDir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0755 {
		t.Errorf("expected custom dir mode 0755 preserved, got %#o", dirInfo.Mode().Perm())
	}

	// 2. Snapshot file matching format must be tightened to 0600
	snapInfo, err := os.Stat(legacySnapshot)
	if err != nil {
		t.Fatalf("failed to stat legacySnapshot: %v", err)
	}
	if snapInfo.Mode().Perm() != 0600 {
		t.Errorf("expected snapshot file mode 0600, got %#o", snapInfo.Mode().Perm())
	}

	// 3. Unrelated document must NOT be touched (remains 0644)
	docInfo, err := os.Stat(unrelatedDoc)
	if err != nil {
		t.Fatalf("failed to stat unrelatedDoc: %v", err)
	}
	if docInfo.Mode().Perm() != 0644 {
		t.Errorf("expected unrelated document mode 0644 preserved, got %#o", docInfo.Mode().Perm())
	}

	// 4. Verify symlinks are NOT followed to external files
	externalDir := t.TempDir()
	externalFile := filepath.Join(externalDir, "external_target.txt")
	_ = os.WriteFile(externalFile, []byte("external"), 0644)
	_ = os.Chmod(externalFile, 0644)

	symlinkPath := filepath.Join(tmpDir, "2026-01-01_120000_symlink_alias.md")
	_ = os.Symlink(externalFile, symlinkPath)

	tightenHandoffPermissions(tmpDir)

	extInfo, err := os.Stat(externalFile)
	if err != nil {
		t.Fatalf("failed to stat externalFile: %v", err)
	}
	if extInfo.Mode().Perm() != 0644 {
		t.Errorf("expected external target file to preserve 0644, but got %#o", extInfo.Mode().Perm())
	}
}

func TestConcurrentCreateHandoff(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Handoff.SaveDir = tmpDir
	cfg.LLM.Enabled = false // deterministic fast local generation

	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			UUID:  "conv-12345",
			Title: "Concurrent Race Test",
		},
		Messages: []extractor.Message{
			{Sender: "human", Text: "Concurrent request"},
		},
	}

	const numGoroutines = 4
	results := make([]*HandoffResult, numGoroutines)
	errs := make([]error, numGoroutines)
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := CreateHandoff(cfg, detail, fmt.Sprintf("account_%d@example.com", idx))
			results[idx] = res
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	seenPaths := make(map[string]bool)
	for i := 0; i < numGoroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d failed: %v", i, errs[i])
		}
		res := results[i]
		if res == nil || res.SnapshotPath == "" {
			t.Fatalf("goroutine %d returned empty snapshot path", i)
		}
		if seenPaths[res.SnapshotPath] {
			t.Fatalf("collision detected! path %s was allocated to multiple concurrent calls", res.SnapshotPath)
		}
		seenPaths[res.SnapshotPath] = true

		info, err := os.Stat(res.SnapshotPath)
		if err != nil {
			t.Fatalf("snapshot file %s missing: %v", res.SnapshotPath, err)
		}
		if info.Size() == 0 {
			t.Fatalf("snapshot file %s is empty", res.SnapshotPath)
		}
	}

	latestLink := filepath.Join(tmpDir, "latest.md")
	target, err := os.Readlink(latestLink)
	if err != nil {
		t.Fatalf("latest.md cannot be read: %v", err)
	}
	if !seenPaths[target] {
		t.Errorf("latest.md points to unexpected path: %s", target)
	}
}

func TestInterruptedSnapshotLeavesNoPartialMD(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Handoff.SaveDir = tmpDir
	cfg.LLM.Enabled = false

	// 1. Create a successful baseline snapshot
	initialDetail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			UUID:  "conv-init",
			Title: "Initial Complete Task",
		},
		Messages: []extractor.Message{
			{Sender: "human", Text: "Initial task"},
		},
	}
	initialRes, err := CreateHandoff(cfg, initialDetail, "user@example.com")
	if err != nil {
		t.Fatalf("initial CreateHandoff failed: %v", err)
	}

	initialBytes, err := os.ReadFile(initialRes.SnapshotPath)
	if err != nil {
		t.Fatalf("cannot read initial snapshot: %v", err)
	}

	// 2. Simulate interrupted creation in a read-only directory
	// Make tmpDir non-writable so that temporary file creation or link fails
	_ = os.Chmod(tmpDir, 0500)

	interruptedDetail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			UUID:  "conv-broken",
			Title: "Interrupted Task",
		},
		Messages: []extractor.Message{
			{Sender: "human", Text: "Interrupted task"},
		},
	}
	_, err = CreateHandoff(cfg, interruptedDetail, "user@example.com")
	if err == nil {
		t.Fatalf("expected error when handoffs dir is read-only, got nil")
	}

	// Restore permission to inspect contents
	_ = os.Chmod(tmpDir, 0700)

	// Verify no partial .md file for "Interrupted Task" was created
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read tmpDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "interrupted-task") {
			t.Errorf("found partial/corrupt snapshot file: %s", e.Name())
		}
	}

	// Verify the existing snapshot is completely intact and uncorrupted
	currentBytes, err := os.ReadFile(initialRes.SnapshotPath)
	if err != nil {
		t.Fatalf("cannot read initial snapshot after interruption: %v", err)
	}
	if !bytes.Equal(initialBytes, currentBytes) {
		t.Errorf("initial snapshot content was modified or corrupted")
	}

	// Verify latest.md still resolves to the initial valid snapshot
	latestLink := filepath.Join(tmpDir, "latest.md")
	target, err := os.Readlink(latestLink)
	if err != nil {
		t.Fatalf("latest.md cannot be read: %v", err)
	}
	if target != initialRes.SnapshotPath {
		t.Errorf("latest.md should still point to initial snapshot %s, got %s", initialRes.SnapshotPath, target)
	}
}
