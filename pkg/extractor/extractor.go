package extractor

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"claude-switch/pkg/leveldb"
	"claude-switch/pkg/v8"
)

type ConversationSummary struct {
	UUID        string `json:"uuid"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Model       string `json:"model"`
	CurrentLeaf string `json:"current_leaf,omitempty"`
	Seq         uint64 `json:"seq"`
	Size        int    `json:"size"`
}

type Artifact struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Language   string `json:"language"`
	Content    string `json:"content"`
}

type Message struct {
	UUID       string     `json:"uuid"`
	ParentUUID string     `json:"parent_uuid,omitempty"`
	Sender     string     `json:"sender"`
	Text       string     `json:"text"`
	CreatedAt  string     `json:"created_at,omitempty"`
	Artifacts  []Artifact `json:"artifacts,omitempty"`
}

type ConversationDetail struct {
	Summary   ConversationSummary `json:"summary"`
	Messages  []Message           `json:"messages"`
	Artifacts []Artifact          `json:"artifacts"`
}

var uuidRegex = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
var timeRegex = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

func ListConversations(profilePath string) ([]ConversationSummary, error) {
	leveldbDir := filepath.Join(profilePath, "IndexedDB", "https_claude.ai_0.indexeddb.leveldb")
	records, err := leveldb.ReadConsistentDatabase(leveldbDir)
	if err != nil {
		return nil, fmt.Errorf("không thể đọc IndexedDB của profile: %w", err)
	}

	convMap := make(map[string]ConversationSummary)

	for _, rec := range records {
		val := rec.Value
		if !bytes.Contains(val, []byte("conversationUuid")) && !bytes.Contains(val, []byte("chat_messages")) {
			continue
		}

		// Find UUID
		uIdx := bytes.Index(val, []byte("conversationUuid\"$"))
		var uuid string
		if uIdx != -1 && uIdx+18+36 <= len(val) {
			uuid = string(val[uIdx+18 : uIdx+18+36])
		} else {
			m := uuidRegex.Find(val)
			if m != nil {
				uuid = string(m)
			}
		}

		if uuid == "" {
			continue
		}

		// Find Name / Title
		title := "(Untitled)"
		nIdx := bytes.Index(val, []byte("\x04name"))
		if nIdx != -1 {
			t, _ := v8.ParseV8String(val, nIdx+5)
			if strings.TrimSpace(t) != "" {
				title = strings.TrimSpace(t)
			}
		}

		// Find UpdatedAt
		var updatedAt string
		upIdx := bytes.Index(val, []byte("updated_at\""))
		if upIdx != -1 {
			m := timeRegex.Find(val[upIdx:])
			if m != nil {
				updatedAt = string(m)
			}
		}

		// Find Model
		var model string
		mIdx := bytes.Index(val, []byte("model\""))
		if mIdx != -1 {
			mod, _ := v8.ParseV8String(val, mIdx+6)
			model = mod
		}

		// Find Current Leaf UUID
		var currentLeaf string
		leafIdx := bytes.Index(val, []byte("current_leaf_message_uuid\"$"))
		if leafIdx != -1 && leafIdx+27+36 <= len(val) {
			currentLeaf = string(val[leafIdx+27 : leafIdx+27+36])
		}

		existing, ok := convMap[uuid]
		// Highest sequence number wins (or latest updated_at)
		if !ok || rec.Seq > existing.Seq || (rec.Seq == existing.Seq && updatedAt > existing.UpdatedAt) {
			convMap[uuid] = ConversationSummary{
				UUID:        uuid,
				Title:       title,
				UpdatedAt:   updatedAt,
				Model:       model,
				CurrentLeaf: currentLeaf,
				Seq:         rec.Seq,
				Size:        len(val),
			}
		}
	}

	// Chromium stores large IndexedDB values outside LevelDB in the sibling
	// .blob directory. The active conversation cache lives there, so scanning
	// LevelDB records alone can return drafts but miss the conversation list.
	for _, conv := range listConversationsFromBlobs(profilePath) {
		existing, ok := convMap[conv.UUID]
		if !ok || conv.UpdatedAt > existing.UpdatedAt {
			convMap[conv.UUID] = conv
		}
	}

	var list []ConversationSummary
	for _, item := range convMap {
		list = append(list, item)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt > list[j].UpdatedAt
	})

	return list, nil
}

func GetConversationDetail(profilePath string, targetUUID string) (*ConversationDetail, error) {
	var bestVal []byte
	var bestSeq uint64

	// 1. Try finding in Chromium's IndexedDB .blob storage first (where full chat payloads are kept)
	blobData, err := findConversationBlob(profilePath, targetUUID)
	if err == nil && len(blobData) > 0 {
		bestVal = blobData
	} else {
		// 2. Fall back to LevelDB records
		leveldbDir := filepath.Join(profilePath, "IndexedDB", "https_claude.ai_0.indexeddb.leveldb")
		records, err := leveldb.ReadConsistentDatabase(leveldbDir)
		if err != nil {
			return nil, fmt.Errorf("không thể đọc IndexedDB của profile: %w", err)
		}

		for _, rec := range records {
			if bytes.Contains(rec.Value, []byte(targetUUID)) {
				if len(bestVal) == 0 || rec.Seq > bestSeq || (rec.Seq == bestSeq && len(rec.Value) > len(bestVal)) {
					bestVal = rec.Value
					bestSeq = rec.Seq
				}
			}
		}
	}

	if len(bestVal) == 0 {
		return nil, fmt.Errorf("conversation %s not found in LevelDB or blob storage", targetUUID)
	}

	detail := &ConversationDetail{
		Summary: ConversationSummary{
			UUID: targetUUID,
			Seq:  bestSeq,
			Size: len(bestVal),
		},
	}

	// Extract Title
	if title := v8.FindV8Field(bestVal, "name"); title != "" {
		detail.Summary.Title = strings.TrimSpace(title)
	}
	if detail.Summary.Title == "" {
		if title := v8.FindV8Field(bestVal, "\x04name"); title != "" {
			detail.Summary.Title = strings.TrimSpace(title)
		}
	}
	if detail.Summary.Title == "" {
		detail.Summary.Title = "(Untitled)"
	}

	// Extract Model
	if mod := v8.FindV8Field(bestVal, "model"); mod != "" {
		detail.Summary.Model = strings.TrimSpace(mod)
	}
	if detail.Summary.Model == "" {
		if mod := v8.FindV8Field(bestVal, "\x05model"); mod != "" {
			detail.Summary.Model = strings.TrimSpace(mod)
		}
	}

	// Extract Current Leaf UUID
	leafIdx := bytes.Index(bestVal, []byte("current_leaf_message_uuid"))
	if leafIdx != -1 {
		str, _ := v8.ParseV8String(bestVal, leafIdx+len("current_leaf_message_uuid"))
		if conversationUUIDPattern.MatchString(str) {
			detail.Summary.CurrentLeaf = str
		}
	}
	if detail.Summary.CurrentLeaf == "" {
		leafIdx := bytes.Index(bestVal, []byte("current_leaf_message_uuid\"$"))
		if leafIdx != -1 && leafIdx+27+36 <= len(bestVal) {
			cand := string(bestVal[leafIdx+27 : leafIdx+27+36])
			if conversationUUIDPattern.MatchString(cand) {
				detail.Summary.CurrentLeaf = cand
			}
		}
	}

	// Extract Artifacts
	detail.Artifacts = extractArtifacts(bestVal)

	// Extract Messages tracing active branch
	msgs, err := extractMessagesWithBranch(bestVal, detail.Summary.CurrentLeaf)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if !utf8.ValidString(m.Text) {
			return nil, fmt.Errorf("tin nhắn %s chứa chuỗi không phải UTF-8 hợp lệ", m.UUID)
		}
	}
	detail.Messages = msgs

	return detail, nil
}

var artifactRegex = regexp.MustCompile(`(?s)<antArtifact\s+([^>]*?)>(.*?)</antArtifact>`)
var attrRegex = regexp.MustCompile(`([a-zA-Z_-]+)="([^"]*)"`)

func extractArtifacts(data []byte) []Artifact {
	str := string(data)
	matches := artifactRegex.FindAllStringSubmatch(str, -1)
	artifactMap := make(map[string]Artifact)

	for _, m := range matches {
		attrsRaw := m[1]
		body := strings.TrimSpace(m[2])

		attrMatches := attrRegex.FindAllStringSubmatch(attrsRaw, -1)
		attrs := make(map[string]string)
		for _, am := range attrMatches {
			attrs[am[1]] = am[2]
		}

		id := attrs["identifier"]
		if id == "" {
			id = attrs["title"]
		}
		if id == "" {
			id = fmt.Sprintf("artifact_%d", len(artifactMap)+1)
		}

		art := Artifact{
			Identifier: id,
			Title:      attrs["title"],
			Type:       attrs["type"],
			Language:   attrs["language"],
			Content:    body,
		}
		// Overwrite with latest version if same identifier
		artifactMap[id] = art
	}

	var res []Artifact
	for _, a := range artifactMap {
		res = append(res, a)
	}
	return res
}

func findMessageUUID(window []byte) (string, int) {
	searchEnd := len(window)
	for searchEnd > 0 {
		uIdx := bytes.LastIndex(window[:searchEnd], []byte("uuid\"$"))
		if uIdx < 0 {
			break
		}
		if uIdx >= 15 && bytes.Equal(window[uIdx-15:uIdx], []byte("parent_message_")) {
			searchEnd = uIdx
			continue
		}
		uActual := uIdx + len("uuid\"$")
		if uActual+36 <= len(window) {
			cand := string(window[uActual : uActual+36])
			if conversationUUIDPattern.MatchString(cand) {
				return cand, uIdx
			}
		}
		searchEnd = uIdx
	}
	searchEnd = len(window)
	for searchEnd > 0 {
		uIdx := bytes.LastIndex(window[:searchEnd], []byte("uuid\""))
		if uIdx < 0 {
			break
		}
		if uIdx >= 15 && bytes.Equal(window[uIdx-15:uIdx], []byte("parent_message_")) {
			searchEnd = uIdx
			continue
		}
		str, _ := v8.ParseV8String(window, uIdx+len("uuid\""))
		if conversationUUIDPattern.MatchString(str) {
			return str, uIdx
		}
		searchEnd = uIdx
	}
	return "", -1
}

func extractMessageText(data []byte) string {
	var textParts []string

	for offset := 0; offset < len(data); {
		pos := bytes.Index(data[offset:], []byte("text"))
		if pos == -1 {
			break
		}
		actual := offset + pos
		offset = actual + 4

		matched := false
		if actual+4 < len(data) {
			tag := data[actual+4]
			if tag == '"' || tag == 'c' {
				str, nextPos := v8.ParseV8String(data, actual+4)
				if len(str) > 0 && str != "text" && str != "content" {
					textParts = append(textParts, str)
					if nextPos > offset {
						offset = nextPos
					}
					matched = true
				}
			}
		}

		if !matched {
			for p := actual + 4; p < len(data) && p < actual+8; p++ {
				if data[p] == '"' || data[p] == 'c' {
					str, nextPos := v8.ParseV8String(data, p)
					if len(str) > 0 && str != "text" && str != "content" {
						textParts = append(textParts, str)
						if nextPos > offset {
							offset = nextPos
						}
						break
					}
				}
			}
		}
	}

	return strings.Join(textParts, "\n\n")
}

func extractMessagesWithBranch(data []byte, currentLeaf string) ([]Message, error) {
	var allMessages []Message
	needle := []byte("sender\"")
	var senderPositions []int
	idx := 0

	for {
		pos := bytes.Index(data[idx:], needle)
		if pos == -1 {
			break
		}
		actual := idx + pos
		senderPositions = append(senderPositions, actual)
		idx = actual + len(needle)
	}

	prevSenderPos := 0

	for i, actual := range senderPositions {
		sender := ""
		tail := data[actual+len(needle):]
		if len(tail) >= 5 {
			if bytes.HasPrefix(tail, []byte("\x05human")) || bytes.HasPrefix(tail, []byte("\"human")) || bytes.HasPrefix(tail, []byte("human")) {
				sender = "human"
			} else if bytes.HasPrefix(tail, []byte("	assistant")) || bytes.HasPrefix(tail, []byte("\"assistant")) || bytes.HasPrefix(tail, []byte("assistant")) {
				sender = "assistant"
			}
		}
		if sender == "" {
			str, _ := v8.ParseV8String(data, actual+len(needle))
			if str == "human" || str == "assistant" {
				sender = str
			}
		}

		if sender == "" {
			continue
		}

		window := data[prevSenderPos:actual]
		prevSenderPos = actual + len(needle)

		// Find Message UUID
		msgUUID, uPos := findMessageUUID(window)

		// Find Parent Message UUID (search forward around sender)
		var parentUUID string
		forwardLen := 1000
		if i+1 < len(senderPositions) {
			forwardLen = senderPositions[i+1] - actual
		} else if actual+forwardLen > len(data) {
			forwardLen = len(data) - actual
		}
		forwardWindow := data[actual : actual+forwardLen]

		pNeedle := []byte("parent_message_uuid\"$")
		pIdx := bytes.Index(forwardWindow, pNeedle)
		if pIdx != -1 && pIdx+len(pNeedle)+36 <= len(forwardWindow) {
			cand := string(forwardWindow[pIdx+len(pNeedle) : pIdx+len(pNeedle)+36])
			if conversationUUIDPattern.MatchString(cand) || strings.HasPrefix(cand, "00000000") {
				parentUUID = cand
			}
		}
		if parentUUID == "" {
			pNeedle2 := []byte("parent_message_uuid\"")
			pIdx2 := bytes.Index(forwardWindow, pNeedle2)
			if pIdx2 != -1 {
				str, _ := v8.ParseV8String(forwardWindow, pIdx2+len(pNeedle2))
				if conversationUUIDPattern.MatchString(str) || strings.HasPrefix(str, "00000000") {
					parentUUID = str
				}
			}
		}

		// Extract text
		contentWindow := window
		if uPos != -1 {
			contentWindow = window[uPos:]
		}
		text := extractMessageText(contentWindow)

		if text == "" {
			text = cleanPrintableText(string(window))
		}
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "")
		}

		if len(text) > 0 || msgUUID != "" {
			allMessages = append(allMessages, Message{
				UUID:       msgUUID,
				ParentUUID: parentUUID,
				Sender:     sender,
				Text:       text,
			})
		}
	}

	if len(allMessages) == 0 {
		clean := cleanPrintableText(string(data))
		if len(clean) > 0 {
			allMessages = append(allMessages, Message{
				Sender: "user_session",
				Text:   clean,
			})
		}
		return allMessages, nil
	}

	// Trace active branch if currentLeaf is provided
	if currentLeaf != "" {
		msgByUUID := make(map[string]Message)
		for _, m := range allMessages {
			if m.UUID != "" {
				msgByUUID[m.UUID] = m
			}
		}

		if _, exists := msgByUUID[currentLeaf]; !exists {
			return nil, fmt.Errorf("không thể xác định nhánh hoạt động: current_leaf_message_uuid %s không tồn tại trong danh sách tin nhắn", currentLeaf)
		}

		var branch []Message
		currUUID := currentLeaf
		visited := make(map[string]bool)

		for currUUID != "" {
			if visited[currUUID] {
				return nil, fmt.Errorf("phát hiện vòng lặp trong nhánh hội thoại tại UUID %s", currUUID)
			}
			visited[currUUID] = true

			msg, found := msgByUUID[currUUID]
			if !found {
				lastUUID := currUUID
				if len(branch) > 0 {
					lastUUID = branch[len(branch)-1].UUID
				}
				return nil, fmt.Errorf("nhánh hội thoại bị gián đoạn: không tìm thấy tin nhắn cha %s (từ tin nhắn %s)", currUUID, lastUUID)
			}
			branch = append(branch, msg)
			if strings.HasPrefix(msg.ParentUUID, "00000000") {
				break
			}
			if msg.ParentUUID == "" {
				return nil, fmt.Errorf("không thể xác định tin nhắn cha của tin nhắn %s (ParentUUID rỗng)", currUUID)
			}
			currUUID = msg.ParentUUID
		}

		if len(branch) > 0 {
			// Reverse to chronological order (root to leaf)
			for i, j := 0, len(branch)-1; i < j; i, j = i+1, j-1 {
				branch[i], branch[j] = branch[j], branch[i]
			}
			return branch, nil
		}
	}

	return allMessages, nil
}

func cleanPrintableText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError {
			continue
		}
		if r >= 32 && r <= 126 || r == '\n' || r == '	' || r > 127 {
			b.WriteRune(r)
		}
	}
	res := b.String()
	if len(res) > 20000 {
		cut := 20000
		for cut > 0 && !utf8.RuneStart(res[cut]) {
			cut--
		}
		res = res[:cut]
	}
	return strings.TrimSpace(res)
}
