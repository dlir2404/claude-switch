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

// findConversationBlob searches the .blob directory for a blob containing the
// target conversation's full payload (including chat_messages).
func findConversationBlob(profilePath, targetUUID string) ([]byte, error) {
	blobDir := filepath.Join(profilePath, "IndexedDB", "https_claude.ai_0.indexeddb.blob")

	var bestData []byte

	_ = filepath.Walk(blobDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || !info.Mode().IsRegular() || info.Size() < 4 || info.Size() > maxIndexedDBBlobBytes {
			return nil
		}

		wrapped, err := os.ReadFile(path)
		if err != nil || len(wrapped) < 4 || !bytes.Equal(wrapped[:3], []byte{0xff, 0x11, 0x02}) {
			return nil
		}

		decomp, err := leveldb.DecompressSnappy(wrapped[3:])
		if err != nil {
			return nil
		}

		if matched := extractConversationObject(decomp, targetUUID); len(matched) > 0 {
			if len(matched) > len(bestData) {
				bestData = matched
			}
		}
		return nil
	})

	if len(bestData) > 0 {
		return bestData, nil
	}
	return nil, os.ErrNotExist
}

// extractConversationObject parses V8/IndexedDB object boundaries within the decompressed
// blob payload and returns the exact object byte slice for targetUUID containing chat_messages.
// If the blob does not contain targetUUID with its own chat_messages, it returns nil.
func extractConversationObject(data []byte, targetUUID string) []byte {
	ranges := scanV8ObjectRanges(data)

	// If V8 object boundaries were found, evaluate candidates strictly within their object boundaries
	if len(ranges) > 0 {
		// First pass: look for top-level conversation records (depth 0)
		for _, r := range ranges {
			if r.depth != 0 {
				continue
			}
			objSlice := data[r.start:r.end]
			if !bytes.Contains(objSlice, []byte("chat_messages")) {
				continue
			}
			if getConversationObjectUUID(objSlice) == targetUUID {
				return objSlice
			}
		}

		// Second pass: look for nested conversation/tree objects (depth > 0)
		for _, r := range ranges {
			if r.depth == 0 {
				continue
			}
			objSlice := data[r.start:r.end]
			if !bytes.Contains(objSlice, []byte("chat_messages")) {
				continue
			}
			if getConversationObjectUUID(objSlice) == targetUUID {
				return objSlice
			}
		}

		return nil
	}

	// Fallback for single raw synthetic payload without V8 object envelope
	if bytes.Contains(data, []byte("chat_messages")) {
		if getConversationObjectUUID(data) == targetUUID {
			return data
		}
	}

	return nil
}

type v8ObjectRange struct {
	start int
	end   int
	depth int
}

func scanV8ObjectRanges(data []byte) []v8ObjectRange {
	var ranges []v8ObjectRange
	type openObj struct {
		start int
		depth int
	}
	var stack []openObj

	pos := 0
	if len(data) >= 2 && data[0] == 0xff {
		pos = 1
		r := bytes.NewReader(data[pos:])
		_, _ = leveldb.ReadVarint32(r)
		consumed := int(r.Size()) - r.Len()
		pos += consumed
		for pos < len(data) && data[pos] == 0 {
			pos++
		}
	}

	for pos < len(data) {
		b := data[pos]

		// 1. OneByteString
		if b == '"' {
			r := bytes.NewReader(data[pos+1:])
			l, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				end := pos + 1 + consumed + int(l)
				if end <= len(data) {
					pos = end
					continue
				}
			}
		}

		// 2. TwoByteString
		if b == 'c' {
			r := bytes.NewReader(data[pos+1:])
			l, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				end := pos + 1 + consumed + int(l)
				if end <= len(data) {
					pos = end
					continue
				}
			}
		}

		// 3. Utf8String
		if b == 'S' {
			r := bytes.NewReader(data[pos+1:])
			l, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				end := pos + 1 + consumed + int(l)
				if end <= len(data) {
					pos = end
					continue
				}
			}
		}

		// 4. Begin Object
		if b == 'o' {
			stack = append(stack, openObj{start: pos, depth: len(stack)})
			pos++
			continue
		}

		// 5. End Object
		if b == '{' {
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				r := bytes.NewReader(data[pos+1:])
				_, err := leveldb.ReadVarint32(r)
				consumed := 0
				if err == nil {
					consumed = int(r.Size()) - r.Len()
				}
				end := pos + 1 + consumed
				ranges = append(ranges, v8ObjectRange{
					start: top.start,
					end:   end,
					depth: top.depth,
				})
				pos = end
				continue
			}
		}

		// 6. Double / Date (8 bytes)
		if b == 'N' || b == 'D' {
			if pos+9 <= len(data) {
				pos += 9
				continue
			}
		}

		// 7. Int32 / Uint32 (tag + varint)
		if b == 'I' || b == 'U' {
			r := bytes.NewReader(data[pos+1:])
			_, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				pos += 1 + consumed
				continue
			}
		}

		// 8. Single byte primitives: True, False, Undefined, Null
		if b == 'T' || b == 'F' || b == '_' || b == '0' {
			pos++
			continue
		}

		// 9. Begin Array: 'A' (Dense JS Array) or 'a' (Sparse JS Array)
		if b == 'A' || b == 'a' {
			r := bytes.NewReader(data[pos+1:])
			_, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				pos += 1 + consumed
				continue
			}
		}

		// 10. End Array: '$' (End Dense JS Array) or '@' (End Sparse JS Array)
		// Followed by two varints: numProperties, then length
		if b == '$' || b == '@' {
			r := bytes.NewReader(data[pos+1:])
			_, err1 := leveldb.ReadVarint32(r)
			if err1 == nil {
				consumed1 := int(r.Size()) - r.Len()
				consumed2 := 0
				r2 := bytes.NewReader(data[pos+1+consumed1:])
				if _, err2 := leveldb.ReadVarint32(r2); err2 == nil {
					consumed2 = int(r2.Size()) - r2.Len()
				}
				pos += 1 + consumed1 + consumed2
				continue
			}
		}

		// 11. Object reference: '^' + varint
		if b == '^' {
			r := bytes.NewReader(data[pos+1:])
			_, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				pos += 1 + consumed
				continue
			}
		}

		// 12. ArrayBuffer: 'B' + varint length + bytes
		if b == 'B' {
			r := bytes.NewReader(data[pos+1:])
			l, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				end := pos + 1 + consumed + int(l)
				if end <= len(data) {
					pos = end
					continue
				}
			}
		}

		// 13. Map / Set endings: ':' or ',' + varint
		if b == ':' || b == ',' {
			r := bytes.NewReader(data[pos+1:])
			_, err := leveldb.ReadVarint32(r)
			if err == nil {
				consumed := int(r.Size()) - r.Len()
				pos += 1 + consumed
				continue
			}
		}

		// 14. Begin Map (';') or Begin Set ('\'')
		if b == ';' || b == '\'' {
			pos++
			continue
		}

		pos++
	}

	return ranges
}

func getConversationObjectUUID(data []byte) string {
	// 1. Check conversationUuid anywhere in object
	cKey := []byte("conversationUuid")
	for offset := 0; offset < len(data); {
		pos := bytes.Index(data[offset:], cKey)
		if pos == -1 {
			break
		}
		actual := offset + pos
		offset = actual + len(cKey)
		if cand := extractFieldUUID(data, actual+len(cKey)); cand != "" {
			return cand
		}
	}

	// 2. Check standalone uuid before chat_messages
	cmIdx := bytes.Index(data, []byte("chat_messages"))
	headerLimit := len(data)
	if cmIdx != -1 {
		headerLimit = cmIdx
	}
	header := data[:headerLimit]

	uKey := []byte("uuid")
	for offset := 0; offset < len(header); {
		pos := bytes.Index(header[offset:], uKey)
		if pos == -1 {
			break
		}
		actual := offset + pos
		offset = actual + len(uKey)

		// Reject parent_message_uuid and current_leaf_message_uuid
		if actual >= 15 && bytes.Equal(header[actual-15:actual], []byte("parent_message_")) {
			continue
		}
		if actual >= 21 && bytes.Equal(header[actual-21:actual], []byte("current_leaf_message_")) {
			continue
		}

		if cand := extractFieldUUID(header, actual+len(uKey)); cand != "" {
			return cand
		}
	}
	return ""
}

// extractFieldUUID extracts a 36-char UUID string following a field key.
func extractFieldUUID(data []byte, pos int) string {
	needle := []byte("\"$")
	if pos+len(needle)+36 <= len(data) && bytes.Equal(data[pos:pos+len(needle)], needle) {
		cand := string(data[pos+len(needle) : pos+len(needle)+36])
		if conversationUUIDPattern.MatchString(cand) {
			return cand
		}
	}
	str, _ := v8.ParseV8String(data, pos)
	if conversationUUIDPattern.MatchString(str) {
		return str
	}
	for p := pos; p < len(data) && p < pos+4; p++ {
		str, _ := v8.ParseV8String(data, p)
		if conversationUUIDPattern.MatchString(str) {
			return str
		}
	}
	return ""
}
