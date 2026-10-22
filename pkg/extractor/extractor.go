package extractor

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
	leveldbDir := filepath.Join(profilePath, "IndexedDB", "https_claude.ai_0.indexeddb.leveldb")
	records, err := leveldb.ReadConsistentDatabase(leveldbDir)
	if err != nil {
		return nil, fmt.Errorf("không thể đọc IndexedDB của profile: %w", err)
	}

	// Find the record matching targetUUID with the highest sequence number
	var bestVal []byte
	var bestSeq uint64

	for _, rec := range records {
		if bytes.Contains(rec.Value, []byte(targetUUID)) {
			if len(bestVal) == 0 || rec.Seq > bestSeq || (rec.Seq == bestSeq && len(rec.Value) > len(bestVal)) {
				bestVal = rec.Value
				bestSeq = rec.Seq
			}
		}
	}

	if len(bestVal) == 0 {
		return nil, fmt.Errorf("conversation %s not found in LevelDB", targetUUID)
	}

	detail := &ConversationDetail{
		Summary: ConversationSummary{
			UUID: targetUUID,
			Seq:  bestSeq,
			Size: len(bestVal),
		},
	}

	// Extract Title
	nIdx := bytes.Index(bestVal, []byte("\x04name"))
	if nIdx != -1 {
		title, _ := v8.ParseV8String(bestVal, nIdx+5)
		detail.Summary.Title = strings.TrimSpace(title)
	}
	if detail.Summary.Title == "" {
		detail.Summary.Title = "(Untitled)"
	}

	// Extract Model
	mIdx := bytes.Index(bestVal, []byte("model\""))
	if mIdx != -1 {
		mod, _ := v8.ParseV8String(bestVal, mIdx+6)
		detail.Summary.Model = mod
	}

	// Extract Current Leaf UUID
	leafIdx := bytes.Index(bestVal, []byte("current_leaf_message_uuid\"$"))
	if leafIdx != -1 && leafIdx+27+36 <= len(bestVal) {
		detail.Summary.CurrentLeaf = string(bestVal[leafIdx+27 : leafIdx+27+36])
	}

	// Extract Artifacts
	detail.Artifacts = extractArtifacts(bestVal)

	// Extract Messages tracing active branch
	msgs, err := extractMessagesWithBranch(bestVal, detail.Summary.CurrentLeaf)
	if err != nil {
		return nil, err
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

func extractMessagesWithBranch(data []byte, currentLeaf string) ([]Message, error) {
	var allMessages []Message
	needle := []byte("sender\"")
	idx := 0
	prevSenderPos := 0

	for {
		pos := bytes.Index(data[idx:], needle)
		if pos == -1 {
			break
		}
		actual := idx + pos
		idx = actual + len(needle)

		sender := ""
		if actual+len(needle)+5 <= len(data) {
			tail := data[actual+len(needle):]
			if bytes.HasPrefix(tail, []byte("\x05human")) || bytes.HasPrefix(tail, []byte("human")) {
				sender = "human"
			} else if bytes.HasPrefix(tail, []byte("	assistant")) || bytes.HasPrefix(tail, []byte("assistant")) {
				sender = "assistant"
			}
		}

		if sender == "" {
			continue
		}

		// The window for this message strictly starts after the previous sender
		startWindow := prevSenderPos
		if actual-65536 > startWindow {
			startWindow = actual - 65536
		}
		window := data[startWindow:actual]
		prevSenderPos = actual + len(needle)

		// Find Message UUID
		var msgUUID string
		uIdx := bytes.LastIndex(window, []byte("uuid\"$"))
		if uIdx != -1 && uIdx+6+36 <= len(window) {
			msgUUID = string(window[uIdx+6 : uIdx+6+36])
		}

		// Find Parent Message UUID (look forward around sender)
		var parentUUID string
		forwardLen := 300
		if actual+forwardLen > len(data) {
			forwardLen = len(data) - actual
		}
		forwardWindow := data[actual : actual+forwardLen]
		pIdx := bytes.Index(forwardWindow, []byte("parent_message_uuid\"$"))
		if pIdx != -1 && pIdx+21+36 <= len(forwardWindow) {
			parentUUID = string(forwardWindow[pIdx+21 : pIdx+21+36])
		}

		// Find text
		typeIdx := bytes.LastIndex(window, []byte("type\"\x04text"))
		if typeIdx == -1 {
			typeIdx = bytes.LastIndex(window, []byte("type\"text"))
		}

		var text string
		if typeIdx != -1 {
			sub := window[:typeIdx]
			textKeyIdx := bytes.LastIndex(sub, []byte("\x04text"))
			if textKeyIdx == -1 {
				textKeyIdx = bytes.LastIndex(sub, []byte("text\""))
			}
			if textKeyIdx != -1 {
				parsed, _ := v8.ParseV8String(sub, textKeyIdx+5)
				text = strings.TrimSpace(parsed)
			}
		}

		if text == "" {
			text = cleanPrintableText(string(window))
		}

		if len(text) > 3 {
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
		if r >= 32 && r <= 126 || r == '\n' || r == '\t' || r > 127 {
			b.WriteRune(r)
		}
	}
	res := b.String()
	if len(res) > 20000 {
		res = res[:20000]
	}
	return strings.TrimSpace(res)
}
