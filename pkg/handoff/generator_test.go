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

func TestHandoffLongMessageWithMiddleConclusionAndAttachments(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Handoff.SaveDir = tmpDir
	cfg.Handoff.TruncateToolResultsChars = 800
	cfg.Handoff.MaxInputTokens = 64000

	att1Content := "# Reviewer Handoff\n" + strings.Repeat("Baseline review details and findings.\n", 300)
	att2Content := "# Phase 1 Fix Plan\n" + strings.Repeat("Action items and fixes plan for CR Agent.\n", 700)

	middleConclusion := "KẾT LUẬN QUAN TRỌNG: Đã pass 10/10 test case, cấu trúc mã nguồn đạt chuẩn."
	vietnameseBoundary := "Kiểm tra kỹ lưỡng các ký tự tiếng Việt đặc thù như đ, ế, ữ, ợ, ắ không bị cắt đôi rune."

	// 5,000 characters of raw tool output inside a code fence
	massiveToolBlock := "```output\n" + strings.Repeat("DEBUG: 2026-10-14 leveldb record dump raw bytes 0x11 0x22\n", 80) + "```"

	longAssistantText := fmt.Sprintf("Bắt đầu thực hiện kiểm tra code và chạy công cụ:\n\n%s\n\nPhân tích chi tiết sau khi chạy công cụ:\n%s\n\n%s\n\nĐề xuất các bước tiếp theo để hoàn thiện.",
		massiveToolBlock, middleConclusion, vietnameseBoundary)

	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			UUID:  "conv-long-test",
			Title: "Long Test with Attachments",
			Model: "claude-sonnet-5-5",
		},
		Attachments: []extractor.Attachment{
			{
				ID:               "att-uuid-1",
				FileName:         "Reviewer Handoff.txt",
				FileType:         "txt",
				ExtractedContent: att1Content,
			},
			{
				ID:               "att-uuid-2",
				FileName:         "Phase 1 Fix Plan.txt",
				FileType:         "txt",
				ExtractedContent: att2Content,
			},
		},
		Messages: []extractor.Message{
			{
				Sender: "human",
				Text:   "Yêu cầu reviewer kiểm tra kỹ lưỡng các thay đổi mới nhất.",
			},
			{
				Sender: "assistant",
				Text:   longAssistantText,
			},
			{
				Sender: "assistant",
				Text:   "", // Empty canceled message
			},
		},
	}

	// 1. Check buildLLMPrompt
	prompt := buildLLMPrompt(cfg, detail, "user@example.com")

	if !strings.Contains(prompt, "--- TÀI LIỆU ĐÍNH KÈM (ATTACHMENTS) ---") {
		t.Errorf("prompt missing Attachments heading")
	}
	if !strings.Contains(prompt, "Reviewer Handoff.txt") || !strings.Contains(prompt, "Phase 1 Fix Plan.txt") {
		t.Errorf("prompt missing attachment titles")
	}
	if !strings.Contains(prompt, "Action items and fixes plan for CR Agent.") {
		t.Errorf("prompt missing attachment content")
	}
	// Middle conclusion MUST be preserved
	if !strings.Contains(prompt, middleConclusion) {
		t.Errorf("middle conclusion was incorrectly truncated from prompt: expected %q", middleConclusion)
	}
	// Tool output block MUST be truncated
	if !strings.Contains(prompt, "...[tool output đã rút gọn]...") {
		t.Errorf("expected tool block to be truncated with notice in prompt")
	}
	// Vietnamese characters must be preserved without UTF-8 corruption
	if !strings.Contains(prompt, vietnameseBoundary) {
		t.Errorf("Vietnamese boundary string was corrupted: expected %q", vietnameseBoundary)
	}
	// Empty message should not leak raw empty brackets
	if strings.Contains(prompt, "[ASSISTANT]: \n\n") {
		t.Errorf("prompt contains empty assistant message block")
	}

	// 2. Check Rule-based Markdown
	md := GenerateRuleBasedMarkdown(cfg, detail, "user@example.com")

	if !strings.Contains(md, "## 1. Tài liệu đính kèm (Attachments)") {
		t.Errorf("markdown missing Attachments section")
	}
	if !strings.Contains(md, middleConclusion) {
		t.Errorf("markdown missing middle conclusion")
	}

	// 3. Check CreateHandoff exports attachments to disk
	res, err := CreateHandoff(cfg, detail, "user@example.com")
	if err != nil {
		t.Fatalf("CreateHandoff failed: %v", err)
	}

	// Verify attachments directory created
	attDir := filepath.Join(tmpDir, "attachments", res.SnapshotPath[len(tmpDir)+1:len(res.SnapshotPath)-3])
	entries, err := os.ReadDir(attDir)
	if err != nil {
		t.Fatalf("failed to read attachments dir %s: %v", attDir, err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 attachment files exported, got %d", len(entries))
	}
}

func TestBudgetExceededPreservesNewestUserMessage(t *testing.T) {
	cfg := config.DefaultConfig()
	// Strict small budget: 500 tokens ~ 2000 runes
	cfg.Handoff.MaxInputTokens = 500
	cfg.Handoff.MaxRecentMessages = 0

	ancientMarker := "FIRST_MESSAGE_MARKER_ANCIENT: This is ancient history from long ago."
	latestMarker := "LATEST_USER_REQUEST_DO_NOT_LOSE: Vui lòng sửa gấp lỗi crash trên production!"

	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			Title: "Budget Allocation Test",
			Model: "claude-sonnet-5-5",
		},
		Attachments: []extractor.Attachment{
			{
				ID:               "att-big-1",
				FileName:         "big_spec.txt",
				FileType:         "txt",
				ExtractedContent: strings.Repeat("Tài liệu đặc tả rất dài có thể chiếm hết token.\n", 150),
			},
		},
		Messages: []extractor.Message{
			{
				Sender: "human",
				Text:   ancientMarker + strings.Repeat(" Chi tiết cũ cần cắt bớt.", 30),
			},
			{
				Sender: "assistant",
				Text:   "Phản hồi từ xa xưa: " + strings.Repeat("Giải thích dài dòng cũ.", 30),
			},
			{
				Sender: "human",
				Text:   "Yêu cầu giữa chừng: " + strings.Repeat("Thông tin thêm.", 20),
			},
			{
				Sender: "assistant",
				Text:   "Phản hồi giữa chừng: " + strings.Repeat("Giải thích thêm.", 20),
			},
			{
				Sender: "human",
				Text:   latestMarker,
			},
		},
	}

	prompt := buildLLMPrompt(cfg, detail, "user@example.com")

	// 1. The LATEST user request MUST BE 100% PRESERVED
	if !strings.Contains(prompt, latestMarker) {
		t.Errorf("prompt lost the newest user request when budget was exceeded! expected %q in prompt", latestMarker)
	}

	// 2. The ancient first message must be dropped to preserve the newest request
	if strings.Contains(prompt, ancientMarker) {
		t.Errorf("expected ancient message to be dropped when budget was exceeded, but it was still present")
	}

	// 3. Clear omission notice must be present
	if !strings.Contains(prompt, "đã được lược bỏ để ưu tiên") {
		t.Errorf("prompt missing omission notice for dropped older messages")
	}

	// 4. Prompt total rune length must respect the token budget
	promptRunes := len([]rune(prompt))
	maxAllowed := cfg.Handoff.MaxInputTokens * 4
	if promptRunes > maxAllowed+200 { // small allowance for header/formatting
		t.Errorf("prompt exceeded budget: got %d runes, max %d runes", promptRunes, maxAllowed)
	}
}

func TestCodeFencesNotTruncatedOnlyToolOutputTruncated(t *testing.T) {
	crucialGoCode := "func CrucialAlgorithmInMiddle() bool {\n    return true\n}"
	crucialDiff := "+ CRUCIAL_DIFF_CHANGE_IN_MIDDLE = 12345"

	// 1. Assistant message with long Go code fence (>2000 chars)
	longGoBlock := "```go\n// Package header\n" + strings.Repeat("// Code line padding\n", 50) +
		crucialGoCode + "\n" + strings.Repeat("// Code tail padding\n", 50) + "```"

	// 2. Assistant message with long Diff code fence (>2000 chars)
	longDiffBlock := "```diff\n--- a/file.go\n+++ b/file.go\n" + strings.Repeat("@@ -1,5 +1,5 @@\n", 40) +
		crucialDiff + "\n" + strings.Repeat("@@ -10,5 +10,5 @@\n", 40) + "```"

	// 3. Assistant message with long Output fence (tool output) (>2000 chars)
	longOutputBlock := "```output\n" + strings.Repeat("RAW TERMINAL DUMP LINE 0x00\n", 80) + "```"

	text := fmt.Sprintf("Dưới đây là mã nguồn và kết quả kiểm tra:\n\n%s\n\n%s\n\n%s\n\nHoàn tất.",
		longGoBlock, longDiffBlock, longOutputBlock)

	res := truncateToolBlocks(text, 500)

	// Go block MUST be preserved completely (not truncated)
	if !strings.Contains(res, crucialGoCode) {
		t.Errorf("Go code fence was incorrectly truncated! expected %q to be preserved", crucialGoCode)
	}

	// Diff block MUST be preserved completely (not truncated)
	if !strings.Contains(res, crucialDiff) {
		t.Errorf("Diff fence was incorrectly truncated! expected %q to be preserved", crucialDiff)
	}

	// Output block MUST be truncated
	if !strings.Contains(res, "...[tool output đã rút gọn]...") {
		t.Errorf("Output fence was not truncated when exceeding limit")
	}
}

func TestAttachmentExportErrorHandlingAndDisambiguation(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Handoff.SaveDir = tmpDir
	cfg.LLM.Enabled = false

	// Test 1: Duplicate filenames must be disambiguated, not overwritten
	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			Title: "Duplicate Files Test",
		},
		Attachments: []extractor.Attachment{
			{
				ID:               "id-1",
				FileName:         "plan.txt",
				FileType:         "txt",
				ExtractedContent: "Plan 1 content",
			},
			{
				ID:               "id-2",
				FileName:         "plan.txt", // Same name!
				FileType:         "txt",
				ExtractedContent: "Plan 2 different content",
			},
		},
		Messages: []extractor.Message{
			{Sender: "human", Text: "Hello"},
		},
	}

	res, err := CreateHandoff(cfg, detail, "user@example.com")
	if err != nil {
		t.Fatalf("CreateHandoff failed: %v", err)
	}

	// Verify both files were created with distinct names
	attDir := filepath.Join(tmpDir, "attachments", res.SnapshotPath[len(tmpDir)+1:len(res.SnapshotPath)-3])
	entries, err := os.ReadDir(attDir)
	if err != nil {
		t.Fatalf("failed to read attachments dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 distinct attachment files, got %d", len(entries))
	}

	names := []string{entries[0].Name(), entries[1].Name()}
	if names[0] == names[1] {
		t.Errorf("filenames were not disambiguated: %v", names)
	}

	// Verify permissions are 0600
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("failed to get file info: %v", err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("expected file mode 0600 for %s, got %#o", entry.Name(), info.Mode().Perm())
		}
	}

	// Test 2: Error handling when save directory is non-writable
	badDir := filepath.Join(tmpDir, "readonly_dir")
	_ = os.MkdirAll(badDir, 0700)
	// Create read-only attachments subfolder
	badAttDir := filepath.Join(badDir, "attachments")
	_ = os.MkdirAll(badAttDir, 0500)
	_ = os.Chmod(badAttDir, 0500)

	cfgBad := config.DefaultConfig()
	cfgBad.Handoff.SaveDir = badDir
	cfgBad.LLM.Enabled = false

	_, errBad := CreateHandoff(cfgBad, detail, "user@example.com")
	if errBad == nil {
		t.Errorf("expected CreateHandoff to fail when attachments directory cannot be written, got nil")
	}
	_ = os.Chmod(badAttDir, 0700)
}
