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

	// Export attachments locally if present with strict verification
	if len(detail.Attachments) > 0 {
		if err := exportAttachmentsToDisk(handoffsDir, snapshotID, detail.Attachments); err != nil {
			return nil, fmt.Errorf("lỗi khi xuất file đính kèm: %w", err)
		}
	}

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

func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, "—", "-")
	name = strings.ReplaceAll(name, ":", "-")
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ' ' {
			b.WriteRune(r)
		}
	}
	res := strings.TrimSpace(b.String())
	if res == "" {
		res = "attachment.txt"
	} else if !strings.HasSuffix(res, ".txt") && !strings.Contains(res, ".") {
		res += ".txt"
	}
	return res
}

func exportAttachmentsToDisk(handoffsDir, snapshotID string, attachments []extractor.Attachment) error {
	if len(attachments) == 0 {
		return nil
	}

	attDir := filepath.Join(handoffsDir, "attachments", snapshotID)
	if err := os.MkdirAll(attDir, 0700); err != nil {
		return fmt.Errorf("không thể tạo thư mục lưu attachment %s: %w", attDir, err)
	}
	_ = os.Chmod(attDir, 0700)

	usedNames := make(map[string]int)

	for i := range attachments {
		att := &attachments[i]
		baseName := sanitizeFileName(att.FileName)
		if baseName == "" {
			baseName = "attachment.txt"
		}

		// Disambiguate duplicate file names
		ext := filepath.Ext(baseName)
		nameWithoutExt := strings.TrimSuffix(baseName, ext)
		usedNames[baseName]++
		count := usedNames[baseName]

		var fileName string
		if count == 1 {
			fileName = baseName
		} else {
			fileName = fmt.Sprintf("%s_%d%s", nameWithoutExt, count, ext)
		}

		filePath := filepath.Join(attDir, fileName)
		contentBytes := []byte(att.ExtractedContent)

		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("không thể tạo file attachment %s: %w", filePath, err)
		}

		n, err := f.Write(contentBytes)
		if err != nil {
			_ = f.Close()
			_ = os.Remove(filePath)
			return fmt.Errorf("lỗi khi ghi nội dung attachment %s: %w", filePath, err)
		}
		if n != len(contentBytes) {
			_ = f.Close()
			_ = os.Remove(filePath)
			return fmt.Errorf("ghi attachment không trọn vẹn: ghi %d/%d bytes vào %s", n, len(contentBytes), filePath)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(filePath)
			return fmt.Errorf("lỗi sync attachment %s: %w", filePath, err)
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(filePath)
			return fmt.Errorf("lỗi đóng file attachment %s: %w", filePath, err)
		}

		// Verify file existence, regularity, and exact size
		info, err := os.Stat(filePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(contentBytes)) {
			_ = os.Remove(filePath)
			return fmt.Errorf("xác minh attachment %s thất bại: file không hợp lệ hoặc kích thước không khớp", filePath)
		}
		if info.Mode().Perm() != 0600 {
			_ = os.Chmod(filePath, 0600)
		}

		att.URL = filePath
	}

	return nil
}

func runeSafeTruncateMiddle(s string, headRunes, tailRunes int, notice string) string {
	r := []rune(s)
	if len(r) <= headRunes+tailRunes {
		return s
	}
	head := string(r[:headRunes])
	tail := string(r[len(r)-tailRunes:])
	return head + notice + tail
}

var toolFenceTagRegex = regexp.MustCompile(`^(?i)(output|stdout|stderr|terminal|console|shell-output|bash-output|tool-output|tool_output|call_result)$`)
var toolBlockFenceRegex = regexp.MustCompile("(?s)(```([a-zA-Z0-9_-]*)\n)(.*?)(```)")
var toolBlockXmlRegex = regexp.MustCompile("(?s)(<(?:antml:call_result|function_results|tool_result|output)[^>]*>)(.*?)(</(?:antml:call_result|function_results|tool_result|output)>)")

func truncateToolBlocks(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}

	// Truncate XML tool results
	text = toolBlockXmlRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := toolBlockXmlRegex.FindStringSubmatch(m)
		if len(sub) == 4 {
			openTag, body, closeTag := sub[1], sub[2], sub[3]
			r := []rune(body)
			if len(r) > maxChars*2 {
				body = runeSafeTruncateMiddle(body, maxChars, maxChars, "\n...[tool output đã rút gọn]...\n")
				return openTag + body + closeTag
			}
		}
		return m
	})

	// Truncate ONLY code fences explicitly tagged as tool outputs/logs (not programming languages like go, diff, ts, etc.)
	text = toolBlockFenceRegex.ReplaceAllStringFunc(text, func(m string) string {
		sub := toolBlockFenceRegex.FindStringSubmatch(m)
		if len(sub) == 5 {
			fullOpen, lang, body, closeTag := sub[1], sub[2], sub[3], sub[4]
			if toolFenceTagRegex.MatchString(strings.TrimSpace(lang)) {
				r := []rune(body)
				if len(r) > maxChars*2 {
					body = runeSafeTruncateMiddle(body, maxChars, maxChars, "\n...[tool output đã rút gọn]...\n")
					return fullOpen + body + closeTag
				}
			}
		}
		return m
	})

	return text
}

func truncateToRuneBudget(text string, budget int, marker string) string {
	if budget <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	markerRunes := []rune(marker)
	if len(markerRunes) >= budget {
		return string(runes[:budget])
	}
	remaining := budget - len(markerRunes)
	head := remaining / 2
	tail := remaining - head
	if tail == 0 {
		return string(runes[:head]) + string(markerRunes)
	}
	return string(runes[:head]) + string(markerRunes) + string(runes[len(runes)-tail:])
}

type promptMessage struct {
	sender string
	text   string
}

func buildRecentMessagesSection(messages []promptMessage, budget int) string {
	const heading = "--- NỘI DUNG TRAO ĐỔI ---\n"
	const omission = "[... Các lượt trao đổi cũ hơn có thể đã được lược bỏ để ưu tiên nội dung gần nhất ...]\n\n"
	if budget <= 0 {
		return ""
	}
	if len([]rune(heading)) > budget {
		return truncateToRuneBudget(heading, budget, "")
	}
	if len(messages) == 0 {
		return heading
	}

	remaining := budget - len([]rune(heading))
	reserveOmission := len(messages) > 1 && remaining > len([]rune(omission))
	if reserveOmission {
		remaining -= len([]rune(omission))
	}
	selected := make(map[int]string)
	omitted := false

	// Reserve space for the latest human request first so a long assistant
	// response cannot crowd the handoff's triggering request out of the prompt.
	latestUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].sender == "human" || messages[i].sender == "user" {
			latestUser = i
			break
		}
	}
	if latestUser >= 0 {
		text := messages[latestUser].text
		size := len([]rune(text))
		if size <= remaining {
			selected[latestUser] = text
			remaining -= size
		} else if remaining > 0 {
			selected[latestUser] = truncateToRuneBudget(text, remaining, "\n...[yêu cầu mới nhất đã được rút gọn]...\n")
			remaining = 0
			omitted = true
		}
	}

	for i := len(messages) - 1; i >= 0; i-- {
		if i == latestUser {
			continue
		}
		message := messages[i].text
		size := len([]rune(message))
		if size <= remaining {
			selected[i] = message
			remaining -= size
			continue
		}
		omitted = true
		if i == len(messages)-1 && remaining > 0 {
			selected[i] = truncateToRuneBudget(message, remaining, "\n...[tin nhắn gần nhất đã được rút gọn]...\n")
			remaining = 0
		}
		break
	}

	var section strings.Builder
	section.WriteString(heading)
	if omitted && reserveOmission {
		section.WriteString(omission)
	}
	for i := range messages {
		if message, ok := selected[i]; ok {
			section.WriteString(message)
		}
	}
	return truncateToRuneBudget(section.String(), budget, "")
}

func buildLLMPrompt(cfg *config.Config, detail *extractor.ConversationDetail, fromAccount string) string {
	maxBudget := 0
	limitConfigured := cfg != nil && cfg.Handoff.MaxInputTokens > 0
	if limitConfigured {
		maxInt := int(^uint(0) >> 1)
		if cfg.Handoff.MaxInputTokens > maxInt/4 {
			maxBudget = maxInt
		} else {
			maxBudget = cfg.Handoff.MaxInputTokens * 4
		}
		// The API request also includes a fixed system prompt, so reserve its
		// estimated size before allocating the user prompt budget.
		systemPromptRunes := len([]rune(llm.SystemHandoffPrompt))
		if maxBudget <= systemPromptRunes {
			return ""
		}
		maxBudget -= systemPromptRunes
	}

	toolMaxChars := 800
	if cfg != nil && cfg.Handoff.TruncateToolResultsChars > 0 {
		toolMaxChars = cfg.Handoff.TruncateToolResultsChars
	}

	header := fmt.Sprintf("Tiêu đề cuộc trò chuyện: %s\nTài khoản nguồn: %s\n", detail.Summary.Title, fromAccount)
	if detail.Summary.Model != "" {
		header += fmt.Sprintf("Model: %s\n", detail.Summary.Model)
	}
	header += "\n"

	rawMsgs := detail.Messages
	if cfg != nil && cfg.Handoff.MaxRecentMessages > 0 && len(rawMsgs) > cfg.Handoff.MaxRecentMessages {
		rawMsgs = rawMsgs[len(rawMsgs)-cfg.Handoff.MaxRecentMessages:]
	}
	readyMsgs := make([]promptMessage, 0, len(rawMsgs))
	for _, message := range rawMsgs {
		text := strings.TrimSpace(message.Text)
		if text == "" {
			if len(message.Attachments) > 0 {
				formatted := fmt.Sprintf("[%s]: (Đã đính kèm %d tài liệu ở phần trên)\n\n", strings.ToUpper(message.Sender), len(message.Attachments))
				readyMsgs = append(readyMsgs, promptMessage{sender: message.Sender, text: formatted})
			}
			continue
		}
		if message.Sender == "assistant" && toolMaxChars > 0 {
			text = truncateToolBlocks(text, toolMaxChars)
		}
		formatted := fmt.Sprintf("[%s]: %s\n\n", strings.ToUpper(message.Sender), text)
		readyMsgs = append(readyMsgs, promptMessage{sender: message.Sender, text: formatted})
	}

	budgeted := limitConfigured
	if budgeted {
		// The setting is a conservative character estimate (four runes per token),
		// not an endpoint tokenizer count. Keep metadata from consuming the prompt.
		headerBudget := maxBudget / 10
		if headerBudget > len([]rune(header)) {
			headerBudget = len([]rune(header))
		}
		header = truncateToRuneBudget(header, headerBudget, "...[header rút gọn]...")
	}

	messageBudget := int(^uint(0) >> 1)
	if budgeted {
		remaining := maxBudget - len([]rune(header))
		messageBudget = remaining
		hasExtras := len(detail.Attachments) > 0 || (cfg != nil && cfg.Handoff.IncludeArtifacts && len(detail.Artifacts) > 0)
		if hasExtras {
			messageBudget = remaining/10*7 + remaining%10*7/10
		}
	}
	messageSection := buildRecentMessagesSection(readyMsgs, messageBudget)

	var artifactBuilder strings.Builder
	if cfg != nil && cfg.Handoff.IncludeArtifacts && len(detail.Artifacts) > 0 {
		artifactBuilder.WriteString("--- CÁC ARTIFACTS / MÃ NGUỒN HIỆN TẠI ---\n")
		for _, artifact := range detail.Artifacts {
			artifactBuilder.WriteString(fmt.Sprintf("### Artifact: %s (%s, %s)\n", artifact.Title, artifact.Identifier, artifact.Language))
			artifactBuilder.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", artifact.Language, artifact.Content))
		}
	}
	artifactSection := artifactBuilder.String()

	var attachmentBuilder strings.Builder
	if len(detail.Attachments) > 0 {
		attachmentBuilder.WriteString("--- TÀI LIỆU ĐÍNH KÈM (ATTACHMENTS) ---\n")
		for i, attachment := range detail.Attachments {
			title := attachment.FileName
			if title == "" {
				title = fmt.Sprintf("Tài liệu #%d", i+1)
			}
			meta := []string{fmt.Sprintf("Loại: %s", attachment.FileType)}
			if attachment.ID != "" {
				meta = append(meta, fmt.Sprintf("ID: %s", attachment.ID))
			}
			if attachment.URL != "" {
				meta = append(meta, fmt.Sprintf("Đường dẫn: %s", attachment.URL))
			}
			attachmentBuilder.WriteString(fmt.Sprintf("### [%s] (%s)\n", title, strings.Join(meta, ", ")))
			attachmentBuilder.WriteString(strings.TrimSpace(attachment.ExtractedContent) + "\n\n")
		}
	}
	attachmentSection := attachmentBuilder.String()

	if budgeted {
		extraBudget := maxBudget - len([]rune(header)) - len([]rune(messageSection))
		if extraBudget < 0 {
			extraBudget = 0
		}
		attachmentBudget, artifactBudget := 0, 0
		switch {
		case attachmentSection != "" && artifactSection != "":
			attachmentBudget = extraBudget/3*2 + extraBudget%3*2/3
			artifactBudget = extraBudget - attachmentBudget
			if len([]rune(attachmentSection)) <= attachmentBudget {
				artifactBudget += attachmentBudget - len([]rune(attachmentSection))
				attachmentBudget = len([]rune(attachmentSection))
			}
			if len([]rune(artifactSection)) <= artifactBudget {
				attachmentBudget += artifactBudget - len([]rune(artifactSection))
				artifactBudget = len([]rune(artifactSection))
			}
		case attachmentSection != "":
			attachmentBudget = extraBudget
		case artifactSection != "":
			artifactBudget = extraBudget
		}
		attachmentSection = truncateToRuneBudget(attachmentSection, attachmentBudget, "\n...[tài liệu đính kèm đã rút gọn]...\n")
		artifactSection = truncateToRuneBudget(artifactSection, artifactBudget, "\n...[artifact đã rút gọn]...\n")
	}

	var prompt strings.Builder
	prompt.WriteString(header)
	if attachmentSection != "" {
		prompt.WriteString(attachmentSection)
	}
	if artifactSection != "" {
		prompt.WriteString(artifactSection)
	}
	prompt.WriteString(messageSection)
	return prompt.String()
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

	sectionNum := 1

	// 1. Attachments Section
	if len(detail.Attachments) > 0 {
		b.WriteString(fmt.Sprintf("\n## %d. Tài liệu đính kèm (Attachments)\n", sectionNum))
		sectionNum++
		for _, att := range detail.Attachments {
			title := att.FileName
			if title == "" {
				title = "Tài liệu đính kèm"
			}
			b.WriteString(fmt.Sprintf("### %s\n", title))
			if att.ID != "" {
				b.WriteString(fmt.Sprintf("- **ID**: `%s`\n", att.ID))
			}
			if att.FileType != "" {
				b.WriteString(fmt.Sprintf("- **Loại file**: `%s`\n", att.FileType))
			}
			if att.URL != "" {
				b.WriteString(fmt.Sprintf("- **Đường dẫn cục bộ**: `%s`\n", att.URL))
			}
			b.WriteString(fmt.Sprintf("- **Nội dung trích xuất**:\n```%s\n%s\n```\n\n", att.FileType, strings.TrimSpace(att.ExtractedContent)))
		}
	}

	includeArtifacts := true
	if cfg != nil {
		includeArtifacts = cfg.Handoff.IncludeArtifacts
	}

	if includeArtifacts && len(detail.Artifacts) > 0 {
		b.WriteString(fmt.Sprintf("\n## %d. Toàn bộ Artifacts & Mã nguồn hoàn thiện\n", sectionNum))
		sectionNum++
		for _, art := range detail.Artifacts {
			lang := art.Language
			if lang == "" {
				lang = art.Type
			}
			b.WriteString(fmt.Sprintf("### %s (%s)\n", art.Title, art.Identifier))
			b.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", lang, art.Content))
		}
	}

	b.WriteString(fmt.Sprintf("\n## %d. Diễn biến các lượt thảo luận gần nhất\n", sectionNum))
	sectionNum++

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
		text := strings.TrimSpace(m.Text)
		if text == "" {
			if len(m.Attachments) > 0 {
				b.WriteString(fmt.Sprintf("**%s**:\n*(Đã đính kèm %d tài liệu ở phần trên)*\n\n", role, len(m.Attachments)))
			}
			continue
		}
		b.WriteString(fmt.Sprintf("**%s**:\n%s\n\n", role, text))
	}

	b.WriteString(fmt.Sprintf("## %d. Bước tiếp theo\n", sectionNum))
	b.WriteString("Hãy tiếp tục giải quyết yêu cầu kỹ thuật đang dang dở dựa trên các tài liệu, artifacts và bối cảnh ở trên.\n")

	return b.String()
}
