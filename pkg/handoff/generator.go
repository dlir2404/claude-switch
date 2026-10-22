package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"claude-switch/pkg/config"
	"claude-switch/pkg/extractor"
	"claude-switch/pkg/llm"
)

var slugRegex = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "session"
	}
	return s
}

var snapshotFileRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_\d{4,6}_.*\.md$`)

func tightenHandoffPermissions(dir string) {
	// Only chmod the directory itself if it is the dedicated ~/.claude-switch/handoffs directory
	if filepath.Clean(dir) == filepath.Clean(config.GetHandoffsDir()) {
		_ = os.Chmod(dir, 0700)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		// Only touch files matching claude-switch snapshot format (never touch user's other documents)
		if snapshotFileRegex.MatchString(e.Name()) && e.Name() != "latest.md" {
			filePath := filepath.Join(dir, e.Name())
			fi, err := os.Lstat(filePath)
			if err != nil {
				continue
			}
			// Only chmod regular files; skip any symlinks to prevent following to external targets
			if fi.Mode().IsRegular() {
				_ = os.Chmod(filePath, 0600)
			}
		}
	}
}

func CreateHandoff(cfg *config.Config, detail *extractor.ConversationDetail, fromAccount string) (*HandoffResult, error) {
	handoffsDir := cfg.Handoff.SaveDir
	if handoffsDir == "" {
		handoffsDir = config.GetHandoffsDir()
	}
	if err := os.MkdirAll(handoffsDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create handoffs dir: %w", err)
	}
	tightenHandoffPermissions(handoffsDir)

	now := time.Now()
	timestampStr := now.Format("2006-01-02_150405")
	slug := slugify(detail.Summary.Title)
	snapshotID := fmt.Sprintf("%s_%s", timestampStr, slug)

	var markdownContent string
	generatorType := "rule_based"

	// Try LLM generation if enabled
	if cfg.LLM.Enabled {
		prompt := buildLLMPrompt(cfg, detail, fromAccount)
		summary, err := llm.GenerateHandoffSummary(cfg, prompt)
		if err == nil && strings.TrimSpace(summary) != "" {
			markdownContent = summary
			generatorType = "llm"
		}
	}

	// Fallback to Rule-based if LLM was disabled or failed
	if markdownContent == "" {
		markdownContent = GenerateRuleBasedMarkdown(cfg, detail, fromAccount)
		if cfg.LLM.Enabled {
			generatorType = "rule_based_fallback"
		}
	}

	// Prepend clean metadata header
	metaHeader := fmt.Sprintf(`<!--
snapshot_id: %s
timestamp: %s
source_account: %s
conversation_uuid: %s
title: %s
model: %s
generator: %s
-->

`, snapshotID, now.Format(time.RFC3339), fromAccount, detail.Summary.UUID, detail.Summary.Title, detail.Summary.Model, generatorType)

	finalContent := metaHeader + markdownContent
	contentBytes := []byte(finalContent)

	// 1. Create a unique hidden temporary file in the same directory with mode 0600
	tempName := fmt.Sprintf(".snapshot-%s-%d.tmp", snapshotID, time.Now().UnixNano())
	tempPath := filepath.Join(handoffsDir, tempName)
	tmpFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("không thể tạo file snapshot tạm: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	// 2. Write full content, verify byte count, Sync, and Close before publishing
	n, err := tmpFile.Write(contentBytes)
	if err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("không thể ghi nội dung snapshot: %w", err)
	}
	if n != len(contentBytes) {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("ghi nội dung snapshot không trọn vẹn: ghi %d/%d bytes", n, len(contentBytes))
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("không thể sync snapshot tạm: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("không thể đóng snapshot tạm: %w", err)
	}

	// 3. Atomically publish to final destination using hard link (atomic no-replace)
	var snapshotPath string
	for i := 0; i <= 999; i++ {
		var name string
		if i == 0 {
			name = fmt.Sprintf("%s.md", snapshotID)
		} else {
			name = fmt.Sprintf("%s_%d.md", snapshotID, i)
		}
		candPath := filepath.Join(handoffsDir, name)
		if err := os.Link(tempPath, candPath); err == nil {
			snapshotPath = candPath
			committed = true
			_ = os.Remove(tempPath)
			break
		} else if !os.IsExist(err) {
			return nil, fmt.Errorf("lỗi khi liên kết snapshot vào đích %s: %w", candPath, err)
		}
	}

	if snapshotPath == "" {
		return nil, fmt.Errorf("không thể cấp phát tên snapshot duy nhất cho %s sau 1000 lần thử", snapshotID)
	}

	// 4. Verification: confirm final file exists, is regular, and size matches exactly
	info, err := os.Stat(snapshotPath)
	if err != nil || info.Size() != int64(len(contentBytes)) {
		return nil, fmt.Errorf("snapshot verification failed: file is missing or size mismatch")
	}

	// 5. Atomic symlink for latest.md with strict verification
	latestLink := filepath.Join(handoffsDir, "latest.md")
	tmpLink := filepath.Join(handoffsDir, fmt.Sprintf(".latest.tmp.%d", time.Now().UnixNano()))
	_ = os.Remove(tmpLink)
	if err := os.Symlink(snapshotPath, tmpLink); err != nil {
		return nil, fmt.Errorf("không thể tạo symlink tạm cho latest.md: %w", err)
	}
	if err := os.Rename(tmpLink, latestLink); err != nil {
		_ = os.Remove(tmpLink)
		return nil, fmt.Errorf("không thể cập nhật symlink latest.md: %w", err)
	}
	targetStat, err := os.Stat(latestLink)
	if err != nil || targetStat.Size() == 0 {
		return nil, fmt.Errorf("xác minh symlink latest.md thất bại: không trỏ tới file hợp lệ: %v", err)
	}

	// 6. Set clipboard directive
	directive := BuildDirective(latestLink)
	clipboardErr := SetClipboard(directive)

	// Trigger background pruning
	go func() {
		_, _ = PruneHandoffs(handoffsDir, &cfg.Handoff.Pruning, false)
	}()

	res := &HandoffResult{
		Status:       StatusSuccess,
		SnapshotPath: snapshotPath,
		Title:        detail.Summary.Title,
		Directive:    directive,
	}

	if clipboardErr != nil {
		res.Status = StatusClipboardFailed
		res.Error = fmt.Sprintf("failed to update clipboard: %v", clipboardErr)
	}

	return res, nil
}

func buildLLMPrompt(cfg *config.Config, detail *extractor.ConversationDetail, fromAccount string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Tiêu đề cuộc trò chuyện: %s\n", detail.Summary.Title))
	b.WriteString(fmt.Sprintf("Tài khoản nguồn: %s\n", fromAccount))
	b.WriteString(fmt.Sprintf("Model: %s\n\n", detail.Summary.Model))

	b.WriteString("--- NỘI DUNG TRAO ĐỔI ---\n")
	messages := detail.Messages
	if cfg != nil && cfg.Handoff.MaxRecentMessages > 0 && len(messages) > cfg.Handoff.MaxRecentMessages {
		messages = messages[len(messages)-cfg.Handoff.MaxRecentMessages:]
	}

	for _, m := range messages {
		text := m.Text
		if cfg != nil && cfg.Handoff.TruncateToolResultsChars > 0 && len(text) > cfg.Handoff.TruncateToolResultsChars*2 {
			text = text[:cfg.Handoff.TruncateToolResultsChars] + "\n...[đã rút gọn]...\n" + text[len(text)-cfg.Handoff.TruncateToolResultsChars:]
		}
		b.WriteString(fmt.Sprintf("[%s]: %s\n\n", strings.ToUpper(m.Sender), text))
	}

	if cfg != nil && cfg.Handoff.IncludeArtifacts && len(detail.Artifacts) > 0 {
		b.WriteString("\n--- CÁC ARTIFACTS / MÃ NGUỒN HIỆN TẠI ---\n")
		for _, art := range detail.Artifacts {
			b.WriteString(fmt.Sprintf("### Artifact: %s (%s, %s)\n", art.Title, art.Identifier, art.Language))
			b.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", art.Language, art.Content))
		}
	}

	prompt := b.String()
	if cfg != nil && cfg.Handoff.MaxInputTokens > 0 && len(prompt) > cfg.Handoff.MaxInputTokens*4 {
		prompt = prompt[:cfg.Handoff.MaxInputTokens*4]
	}
	return prompt
}

func GenerateRuleBasedMarkdown(cfg *config.Config, detail *extractor.ConversationDetail, fromAccount string) string {
	var b strings.Builder
	now := time.Now().Format("2006-01-02 15:04:05")

	b.WriteString(fmt.Sprintf("# Session Handoff: %s\n", detail.Summary.Title))
	b.WriteString(fmt.Sprintf("- **Thời gian xuất**: %s\n", now))
	b.WriteString(fmt.Sprintf("- **Tài khoản nguồn**: %s\n", fromAccount))
	if detail.Summary.Model != "" {
		b.WriteString(fmt.Sprintf("- **Model**: %s\n", detail.Summary.Model))
	}

	includeArtifacts := true
	if cfg != nil {
		includeArtifacts = cfg.Handoff.IncludeArtifacts
	}

	if includeArtifacts && len(detail.Artifacts) > 0 {
		b.WriteString("\n## 1. Toàn bộ Artifacts & Mã nguồn hoàn thiện\n")
		for _, art := range detail.Artifacts {
			lang := art.Language
			if lang == "" {
				lang = art.Type
			}
			b.WriteString(fmt.Sprintf("### %s (%s)\n", art.Title, art.Identifier))
			b.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", lang, art.Content))
		}
	}

	b.WriteString("\n## 2. Diễn biến các lượt thảo luận gần nhất\n")
	messages := detail.Messages
	limit := 6
	if cfg != nil && cfg.Handoff.MaxRecentMessages > 0 {
		limit = cfg.Handoff.MaxRecentMessages
	}
	if len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}

	for _, m := range messages {
		role := "User"
		if m.Sender == "assistant" {
			role = "Claude"
		}
		b.WriteString(fmt.Sprintf("**%s**:\n%s\n\n", role, m.Text))
	}

	b.WriteString("## 3. Bước tiếp theo\n")
	b.WriteString("Hãy tiếp tục giải quyết yêu cầu kỹ thuật đang dang dở dựa trên các artifacts và bối cảnh ở trên.\n")

	return b.String()
}
