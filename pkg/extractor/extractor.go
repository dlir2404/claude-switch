package extractor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"claude-switch/pkg/leveldb"
	"claude-switch/pkg/v8"
)

type ConversationSummary struct {
	UUID      string `json:"uuid"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Model     string `json:"model"`
	Size      int    `json:"size"`
}

type Artifact struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Language   string `json:"language"`
	Content    string `json:"content"`
}

type Message struct {
	UUID      string   `json:"uuid"`
	Sender    string   `json:"sender"`
	Text      string   `json:"text"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}

type ConversationDetail struct {
	Summary   ConversationSummary `json:"summary"`
	Messages  []Message           `json:"messages"`
	Artifacts []Artifact          `json:"artifacts"`
}

var uuidRegex = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
var timeRegex = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

func getLogPath(profilePath string) string {
	leveldbDir := filepath.Join(profilePath, "IndexedDB", "https_claude.ai_0.indexeddb.leveldb")
	entries, err := os.ReadDir(leveldbDir)
	if err != nil {
		return ""
	}
	var logFile string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			logFile = filepath.Join(leveldbDir, entry.Name())
		}
	}
	return logFile
}

func ListConversations(profilePath string) ([]ConversationSummary, error) {
	logPath := getLogPath(profilePath)
	if logPath == "" {
		return nil, fmt.Errorf("no LevelDB log file found in %s", profilePath)
	}

	puts, err := leveldb.ReadWALPuts(logPath)
	if err != nil {
		return nil, err
	}

	convMap := make(map[string]ConversationSummary)

	for _, kv := range puts {
		val := kv.Value
		if !bytes.Contains(val, []byte("conversationUuid")) && !bytes.Contains(val, []byte("chat_messages")) {
			continue
		}

		// Find UUID
		uIdx := bytes.Index(val, []byte("conversationUuid\"$"))
		var uuid string
		if uIdx != -1 && uIdx+36+18 <= len(val) {
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

		existing, ok := convMap[uuid]
		if !ok || len(val) > existing.Size || updatedAt > existing.UpdatedAt {
			convMap[uuid] = ConversationSummary{
				UUID:      uuid,
				Title:     title,
				UpdatedAt: updatedAt,
				Model:     model,
				Size:      len(val),
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
	logPath := getLogPath(profilePath)
	if logPath == "" {
		return nil, fmt.Errorf("no LevelDB log file found in %s", profilePath)
	}

	puts, err := leveldb.ReadWALPuts(logPath)
	if err != nil {
		return nil, err
	}

	// Find the largest / latest put matching targetUUID
	var bestVal []byte
	for _, kv := range puts {
		if bytes.Contains(kv.Value, []byte(targetUUID)) {
			if len(kv.Value) > len(bestVal) {
				bestVal = kv.Value
			}
		}
	}

	if len(bestVal) == 0 {
		return nil, fmt.Errorf("conversation %s not found in LevelDB", targetUUID)
	}

	detail := &ConversationDetail{
		Summary: ConversationSummary{
			UUID: targetUUID,
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

	// Extract Artifacts
	detail.Artifacts = extractArtifacts(bestVal)

	// Extract Messages
	detail.Messages = extractMessages(bestVal)

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

func extractMessages(data []byte) []Message {
	var messages []Message

	needle := []byte("sender\"")
	idx := 0

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

		// Look backward up to 64KB for content block
		startWindow := actual - 65536
		if startWindow < 0 {
			startWindow = 0
		}
		window := data[startWindow:actual]

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
			messages = append(messages, Message{
				Sender: sender,
				Text:   text,
			})
		}
	}

	if len(messages) == 0 {
		clean := cleanPrintableText(string(data))
		if len(clean) > 0 {
			messages = append(messages, Message{
				Sender: "user_session",
				Text:   clean,
			})
		}
	}

	return messages
}

func cleanPrintableText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 32 && r <= 126 || r == '\n' || r == '\t' || r > 127 {
			b.WriteRune(r)
		}
	}
	res := b.String()
	// limit single chunk to reasonable preview
	if len(res) > 20000 {
		res = res[:20000]
	}
	return strings.TrimSpace(res)
}
