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

func CreateHandoff(cfg *config.Config, detail *extractor.ConversationDetail, fromAccount string) (string, error) {
	handoffsDir := cfg.Handoff.SaveDir
	if handoffsDir == "" {
		handoffsDir = config.GetHandoffsDir()
	}
	if err := os.MkdirAll(handoffsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create handoffs dir: %w", err)
	}

	var markdownContent string
	var genErr error

	// Try LLM generation if enabled
	if cfg.LLM.Enabled {
		prompt := buildLLMPrompt(cfg, detail, fromAccount)
		summary, err := llm.GenerateHandoffSummary(cfg, prompt)
		if err == nil && strings.TrimSpace(summary) != "" {
			markdownContent = summary
		} else {
			genErr = err
		}
	}

	// Fallback to Rule-based if LLM was disabled or failed
	if markdownContent == "" {
		markdownContent = GenerateRuleBasedMarkdown(cfg, detail, fromAccount)
		if genErr != nil {
			markdownContent = fmt.Sprintf("<!-- Note: LLM summary fallback (%v) -->\n\n%s", genErr, markdownContent)
		}
	}

	// Create snapshot file
	now := time.Now()
	timestampStr := now.Format("2006-01-02_150405")
	slug := slugify(detail.Summary.Title)
	filename := fmt.Sprintf("%s_%s.md", timestampStr, slug)
	snapshotPath := filepath.Join(handoffsDir, filename)

	if _, err := os.Stat(snapshotPath); err == nil {
		for i := 1; i <= 99; i++ {
			altName := fmt.Sprintf("%s_%s_%d.md", timestampStr, slug, i)
			altPath := filepath.Join(handoffsDir, altName)
			if _, err := os.Stat(altPath); os.IsNotExist(err) {
				snapshotPath = altPath
				break
			}
		}
	}

	if err := os.WriteFile(snapshotPath, []byte(markdownContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write snapshot file: %w", err)
	}

	// Update latest.md symlink
	latestLink := filepath.Join(handoffsDir, "latest.md")
	_ = os.Remove(latestLink)
	if err := os.Symlink(snapshotPath, latestLink); err != nil {
		return "", fmt.Errorf("failed to create latest symlink: %w", err)
	}

	// Set macOS clipboard directive
	directive := BuildDirective(latestLink)
	_ = SetClipboard(directive)

	// Trigger background pruning
	go func() {
		_, _ = PruneHandoffs(handoffsDir, &cfg.Handoff.Pruning, false)
	}()

	return snapshotPath, nil
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
