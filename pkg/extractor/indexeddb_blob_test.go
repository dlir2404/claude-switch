package extractor

import (
	"testing"
)

func TestExtractConversationSummariesFromBlob(t *testing.T) {
	const (
		conversationID = "12345678-1234-1234-1234-123456789abc"
		leafID         = "abcdefab-cdef-abcd-efab-cdefabcdefab"
	)
	data := appendV8TestString(nil, "uuid", conversationID)
	data = appendV8TestString(data, "name", "A conversation title")
	data = appendV8TestString(data, "updated_at", "2026-10-22T10:15:30.000Z")
	data = appendV8TestString(data, "model", "claude-sonnet")
	data = appendV8TestString(data, "current_leaf_message_uuid", leafID)

	got := extractConversationSummariesFromBlob(data)
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	if got[0].UUID != conversationID || got[0].CurrentLeaf != leafID {
		t.Fatalf("got IDs %q / %q, want %q / %q", got[0].UUID, got[0].CurrentLeaf, conversationID, leafID)
	}
	if got[0].Title != "A conversation title" || got[0].UpdatedAt != "2026-10-22T10:15:30.000Z" || got[0].Model != "claude-sonnet" {
		t.Fatalf("got summary metadata %#v", got[0])
	}
}

func TestExtractConversationSummariesFromBlobSkipsUnanchoredFields(t *testing.T) {
	data := appendV8TestString(nil, "uuid", "12345678-1234-1234-1234-123456789abc")
	data = appendV8TestString(data, "name", "Orphaned metadata")
	data = appendV8TestString(data, "current_leaf_message_uuid", "not-a-uuid")

	if got := extractConversationSummariesFromBlob(data); len(got) != 0 {
		t.Fatalf("got %d summaries, want none", len(got))
	}
}

func appendV8TestString(data []byte, key, value string) []byte {
	data = append(data, key...)
	data = append(data, '"', byte(len(value)))
	return append(data, value...)
}
