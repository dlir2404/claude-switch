package extractor

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"claude-switch/pkg/leveldb"
	"claude-switch/pkg/v8"
)

var conversationUUIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var serializedDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

const maxIndexedDBBlobBytes = 64 << 20
const conversationRecordWindow = 4096

// listConversationsFromBlobs reads Chromium's externally stored IndexedDB
// values. Large V8-serialized values are stored in this directory rather than
// in the LevelDB value itself.
func listConversationsFromBlobs(profilePath string) []ConversationSummary {
	blobDir := filepath.Join(profilePath, "IndexedDB", "https_claude.ai_0.indexeddb.blob")
	var conversations []ConversationSummary
	_ = filepath.Walk(blobDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || !info.Mode().IsRegular() || info.Size() < 4 || info.Size() > maxIndexedDBBlobBytes {
			return nil
		}

		wrapped, err := os.ReadFile(path)
		if err != nil || len(wrapped) < 4 || !bytes.Equal(wrapped[:3], []byte{0xff, 0x11, 0x02}) {
			return nil
		}
		serialized, err := leveldb.DecompressSnappy(wrapped[3:])
		if err != nil {
			return nil
		}
		conversations = append(conversations, extractConversationSummariesFromBlob(serialized)...)
		return nil
	})
	return conversations
}

// extractConversationSummariesFromBlob uses current_leaf_message_uuid as an
// anchor: that field occurs once in each serialized conversation summary and
// keeps nearby generic keys such as "uuid" and "name" associated with the
// same object. It intentionally extracts only summary metadata, not messages.
func extractConversationSummariesFromBlob(data []byte) []ConversationSummary {
	const leafKey = "current_leaf_message_uuid"
	var summaries []ConversationSummary
	for offset := 0; offset < len(data); {
		found := bytes.Index(data[offset:], []byte(leafKey))
		if found < 0 {
			break
		}
		keyPos := offset + found
		offset = keyPos + len(leafKey)

		leaf, _ := v8.ParseV8String(data, keyPos+len(leafKey))
		if !conversationUUIDPattern.MatchString(leaf) {
			continue
		}

		start := keyPos - conversationRecordWindow
		if start < 0 {
			start = 0
		}
		window := data[start:keyPos]
		uuid := previousV8Field(window, "uuid", func(value string) bool {
			return conversationUUIDPattern.MatchString(value)
		})
		updatedAt := previousV8Field(window, "updated_at", func(value string) bool {
			return serializedDatePattern.MatchString(value)
		})
		if uuid == "" || updatedAt == "" {
			continue
		}

		title := previousV8Field(window, "name", func(value string) bool {
			value = strings.TrimSpace(value)
			return value != "" && len(value) <= 512 && !strings.ContainsRune(value, '\u0000')
		})
		if title == "" {
			title = "(Untitled)"
		}
		model := previousV8Field(window, "model", func(value string) bool {
			return value != "" && len(value) <= 128
		})

		summaries = append(summaries, ConversationSummary{
			UUID:        uuid,
			Title:       title,
			UpdatedAt:   updatedAt,
			Model:       model,
			CurrentLeaf: leaf,
		})
	}
	return summaries
}

// previousV8Field scans backwards for the nearest field occurrence whose
// immediately serialized value is a V8 string satisfying accept.
func previousV8Field(data []byte, field string, accept func(string) bool) string {
	searchEnd := len(data)
	for searchEnd > 0 {
		index := bytes.LastIndex(data[:searchEnd], []byte(field))
		if index < 0 {
			return ""
		}
		value, _ := v8.ParseV8String(data, index+len(field))
		if accept(value) {
			return strings.TrimSpace(value)
		}
		searchEnd = index
	}
	return ""
}
