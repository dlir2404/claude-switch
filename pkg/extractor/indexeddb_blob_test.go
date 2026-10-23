package extractor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	data = append(data, '"', byte(len(key)))
	data = append(data, key...)
	data = append(data, '"', byte(len(value)))
	return append(data, value...)
}

func makeSnappyLiteral(data []byte) []byte {
	var buf bytes.Buffer
	n := len(data)
	v := uint32(n)
	for v >= 0x80 {
		buf.WriteByte(byte(v&0x7f | 0x80))
		v >>= 7
	}
	buf.WriteByte(byte(v))

	if n <= 60 {
		buf.WriteByte(byte((n - 1) << 2))
	} else if n <= 65536 {
		buf.WriteByte(byte(61 << 2))
		buf.WriteByte(byte((n - 1) & 0xff))
		buf.WriteByte(byte((n - 1) >> 8))
	}
	buf.Write(data)
	return buf.Bytes()
}

func TestGetConversationDetailFromBlobStorage(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	convUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	leafUUID := "01a13dd1-417a-7d7b-b856-acf6a3716ee3"
	msg1UUID := "01a13ad7-b64b-758f-8fcf-aab77f6277c2"

	payloadBytes := appendV8TestString(nil, "conversationUuid", convUUID)
	payloadBytes = appendV8TestString(payloadBytes, "name", "Review code agent")
	payloadBytes = appendV8TestString(payloadBytes, "model", "claude-sonnet-5-5")
	payloadBytes = appendV8TestString(payloadBytes, "current_leaf_message_uuid", leafUUID)
	payloadBytes = append(payloadBytes, []byte("chat_messages")...)

	var payload bytes.Buffer
	payload.Write(payloadBytes)

	// Message 1 (human)
	payload.Write(buildSyntheticMessage(msg1UUID, "00000000-0000-4000-8000-000000000000", "human", "Please review my code"))
	// Message 2 (assistant, leaf)
	payload.Write(buildSyntheticMessage(leafUUID, msg1UUID, "assistant", "Here is the code review"))

	compressed := makeSnappyLiteral(payload.Bytes())
	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(compressed)

	blobFile := filepath.Join(blobDir, "test_blob")
	if err := os.WriteFile(blobFile, fileData.Bytes(), 0644); err != nil {
		t.Fatalf("failed to write blob file: %v", err)
	}

	detail, err := GetConversationDetail(tempDir, convUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed: %v", err)
	}

	if detail.Summary.Title != "Review code agent" {
		t.Errorf("expected title 'Review code agent', got %q", detail.Summary.Title)
	}
	if detail.Summary.Model != "claude-sonnet-5-5" {
		t.Errorf("expected model 'claude-sonnet-5-5', got %q", detail.Summary.Model)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(detail.Messages))
	}
	if detail.Messages[0].Text != "Please review my code" {
		t.Errorf("expected msg0 text 'Please review my code', got %q", detail.Messages[0].Text)
	}
	if detail.Messages[1].Text != "Here is the code review" {
		t.Errorf("expected msg1 text 'Here is the code review', got %q", detail.Messages[1].Text)
	}
	if detail.Messages[0].Sender != "human" || detail.Messages[1].Sender != "assistant" {
		t.Errorf("unexpected senders: %s, %s", detail.Messages[0].Sender, detail.Messages[1].Sender)
	}
}

func TestFindConversationBlobDisambiguation(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	targetUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	otherUUID := "11111111-2222-3333-4444-555555555555"

	// 1. Create a LARGER blob for OTHER conversation that mentions targetUUID in its text
	var otherPayload bytes.Buffer
	otherPayload.Write(appendV8TestString(nil, "conversationUuid", otherUUID))
	otherPayload.Write(appendV8TestString(nil, "name", "Other Conversation with big text"))
	otherPayload.WriteString("chat_messages")
	// Insert large text mentioning targetUUID
	largeText := fmt.Sprintf("Referencing conversation %s in a large message with lots of padding bytes: %s", targetUUID, strings.Repeat("A", 2000))
	otherPayload.Write(buildSyntheticMessage("msg-other-1", "", "human", largeText))

	otherBlob := filepath.Join(blobDir, "blob_other_large")
	var otherFileData bytes.Buffer
	otherFileData.Write([]byte{0xff, 0x11, 0x02})
	otherFileData.Write(makeSnappyLiteral(otherPayload.Bytes()))
	if err := os.WriteFile(otherBlob, otherFileData.Bytes(), 0644); err != nil {
		t.Fatalf("write other blob failed: %v", err)
	}

	// 2. Create a SMALLER blob for the TARGET conversation
	var targetPayload bytes.Buffer
	targetPayload.Write(appendV8TestString(nil, "conversationUuid", targetUUID))
	targetPayload.Write(appendV8TestString(nil, "name", "Correct Target Conversation"))
	targetPayload.WriteString("chat_messages")
	targetPayload.Write(buildSyntheticMessage("msg-target-1", "00000000-0000-4000-8000-000000000000", "human", "Target text"))

	targetBlob := filepath.Join(blobDir, "blob_target_small")
	var targetFileData bytes.Buffer
	targetFileData.Write([]byte{0xff, 0x11, 0x02})
	targetFileData.Write(makeSnappyLiteral(targetPayload.Bytes()))
	if err := os.WriteFile(targetBlob, targetFileData.Bytes(), 0644); err != nil {
		t.Fatalf("write target blob failed: %v", err)
	}

	// findConversationBlob must select the target blob, NOT the larger other blob!
	data, err := findConversationBlob(tempDir, targetUUID)
	if err != nil {
		t.Fatalf("findConversationBlob failed: %v", err)
	}
	if !bytes.Contains(data, []byte("Correct Target Conversation")) {
		t.Errorf("findConversationBlob picked the wrong blob (wanted target, got other)")
	}

	// When searching for an absent conversation, it must return error and NOT return any other blob
	absentUUID := "99999999-9999-9999-9999-999999999999"
	_, errAbsent := findConversationBlob(tempDir, absentUUID)
	if errAbsent == nil {
		t.Errorf("expected error when searching for absent conversation, got nil")
	}
}

func TestOneByteStringLatin1InConversationDetail(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	convUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	leafUUID := "01a13dd1-417a-7d7b-b856-acf6a3716ee3"
	msgUUID := "01a13ad7-b64b-758f-8fcf-aab77f6277c2"

	var payload bytes.Buffer
	payload.Write(appendV8TestString(nil, "conversationUuid", convUUID))
	payload.Write(appendV8TestString(nil, "name", "Latin-1 Test"))
	payload.Write(appendV8TestString(nil, "current_leaf_message_uuid", leafUUID))
	payload.WriteString("chat_messages")

	// Message with Latin-1 bytes: café (0xe9) and section symbol § (0xa7)
	// In Latin-1: "is removed per spec \xa76.1"
	latin1Text := "is removed per spec \xa76.1 with caf\xe9"
	payload.Write(buildSyntheticMessage(leafUUID, "00000000-0000-4000-8000-000000000000", "assistant", latin1Text))

	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(filepath.Join(blobDir, "test_latin1_blob"), fileData.Bytes(), 0644)

	detail, err := GetConversationDetail(tempDir, convUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed: %v", err)
	}

	if len(detail.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(detail.Messages))
	}

	msg := detail.Messages[0]
	if !utf8.ValidString(msg.Text) {
		t.Errorf("extracted text is not valid UTF-8: %q", msg.Text)
	}
	expectedSubstring := "is removed per spec §6.1 with café"
	if !strings.Contains(msg.Text, expectedSubstring) {
		t.Errorf("expected text to contain %q, got %q", expectedSubstring, msg.Text)
	}
	_ = msgUUID
}

func TestFindConversationBlobTreeRejectsParentMessageUUID(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	targetUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	treeUUID := "22222222-3333-4444-5555-666666666666"

	// Create a blob without conversationUuid, where tree object has uuid = treeUUID,
	// and inside the tree a message has parent_message_uuid = targetUUID.
	var payload bytes.Buffer
	payload.WriteString("tree")
	payload.Write(appendV8TestString(nil, "uuid", treeUUID))
	payload.WriteString("chat_messages")
	// Message has parent_message_uuid = targetUUID
	payload.Write(buildSyntheticMessage("msg-1", targetUUID, "human", "Question"))

	blobFile := filepath.Join(blobDir, "test_tree_parent_blob")
	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(blobFile, fileData.Bytes(), 0644)

	// Searching for targetUUID must NOT match this blob!
	_, err := findConversationBlob(tempDir, targetUUID)
	if err == nil {
		t.Errorf("expected error: blob should not be matched when targetUUID is only a parent_message_uuid, but got nil")
	}

	// Searching for treeUUID SHOULD match this blob!
	data, err := findConversationBlob(tempDir, treeUUID)
	if err != nil {
		t.Fatalf("expected treeUUID %s to match, got error: %v", treeUUID, err)
	}
	if len(data) == 0 {
		t.Fatalf("expected non-empty data for treeUUID")
	}
}

func TestFindConversationBlobMultipleCandidatesInSameBlob(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	cand1UUID := "11111111-1111-1111-1111-111111111111"
	targetUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	leaf1UUID := "01a11111-1111-1111-1111-111111111111"
	leafTargetUUID := "01a12222-2222-2222-2222-222222222222"

	// Construct a blob with two full conversation records: candidate 1 is first, target is second
	var payload bytes.Buffer
	// Record 1
	payload.WriteByte('o')
	payload.Write(appendV8TestString(nil, "conversationUuid", cand1UUID))
	payload.Write(appendV8TestString(nil, "name", "First Candidate"))
	payload.Write(appendV8TestString(nil, "model", "model-1"))
	payload.Write(appendV8TestString(nil, "current_leaf_message_uuid", leaf1UUID))
	payload.WriteString("chat_messages")
	payload.Write(buildSyntheticMessage(leaf1UUID, "00000000-0000-4000-8000-000000000000", "human", "Hello from candidate 1"))
	payload.WriteByte('{')
	payload.WriteByte(1)

	// Record 2 (the target) - place name, model, and current_leaf_message_uuid BEFORE conversationUuid
	payload.WriteByte('o')
	payload.Write(appendV8TestString(nil, "name", "Second Target Candidate"))
	payload.Write(appendV8TestString(nil, "model", "model-target"))
	payload.Write(appendV8TestString(nil, "current_leaf_message_uuid", leafTargetUUID))
	payload.Write(appendV8TestString(nil, "conversationUuid", targetUUID))
	payload.WriteString("chat_messages")
	payload.Write(buildSyntheticMessage(leafTargetUUID, "00000000-0000-4000-8000-000000000000", "human", "Target question content"))
	payload.WriteByte('{')
	payload.WriteByte(1)

	blobFile := filepath.Join(blobDir, "multi_cand_blob")
	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(blobFile, fileData.Bytes(), 0644)

	// 1. Must find targetUUID slice
	data, err := findConversationBlob(tempDir, targetUUID)
	if err != nil {
		t.Fatalf("failed to find target conversation in multi-candidate blob: %v", err)
	}
	if !bytes.Contains(data, []byte("Second Target Candidate")) {
		t.Errorf("expected payload to contain target candidate data")
	}
	if bytes.Contains(data, []byte("First Candidate")) {
		t.Errorf("expected payload to NOT contain candidate 1 data, got mixed payload")
	}

	// 2. GetConversationDetail must return ONLY target candidate's metadata and messages
	detail, err := GetConversationDetail(tempDir, targetUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed: %v", err)
	}
	if detail.Summary.Title != "Second Target Candidate" {
		t.Errorf("expected Title %q, got %q", "Second Target Candidate", detail.Summary.Title)
	}
	if detail.Summary.Model != "model-target" {
		t.Errorf("expected Model %q, got %q", "model-target", detail.Summary.Model)
	}
	if detail.Summary.CurrentLeaf != leafTargetUUID {
		t.Errorf("expected Leaf %q, got %q", leafTargetUUID, detail.Summary.CurrentLeaf)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].Text != "Target question content" {
		t.Errorf("unexpected messages: %#v", detail.Messages)
	}
}

func TestFindConversationBlobSplitMetadataAndChatMessagesInSameBlob(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	targetUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	otherUUID := "11111111-2222-3333-4444-555555555555"

	// Object 1: Metadata of targetUUID, but NO chat_messages
	var payload bytes.Buffer
	payload.WriteByte('o')
	payload.Write(appendV8TestString(nil, "conversationUuid", targetUUID))
	payload.Write(appendV8TestString(nil, "name", "Target Metadata Without Messages"))
	payload.WriteByte('{')
	payload.WriteByte(1)

	// Object 2: chat_messages of otherUUID
	payload.WriteByte('o')
	payload.Write(appendV8TestString(nil, "conversationUuid", otherUUID))
	payload.Write(appendV8TestString(nil, "name", "Other Conversation With Messages"))
	payload.WriteString("chat_messages")
	payload.Write(buildSyntheticMessage("msg-other-1", "00000000-0000-4000-8000-000000000000", "human", "Other conversation text"))
	payload.WriteByte('{')
	payload.WriteByte(1)

	blobFile := filepath.Join(blobDir, "split_metadata_blob")
	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(blobFile, fileData.Bytes(), 0644)

	// Searching for targetUUID must NOT match because object 1 has no chat_messages,
	// and object 2 does not belong to targetUUID.
	_, err := findConversationBlob(tempDir, targetUUID)
	if err == nil {
		t.Errorf("expected error when target metadata and chat_messages are in different objects, but got nil")
	}

	_, errDetail := GetConversationDetail(tempDir, targetUUID)
	if errDetail == nil {
		t.Errorf("expected GetConversationDetail to fail when payload has no chat_messages, but got nil")
	}
}

func TestSynthetic12MessagesUTF8AndBranchValidation(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	convUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	leafUUID := "11111111-0000-0000-0000-000000000012"

	var payload bytes.Buffer
	payload.Write(appendV8TestString(nil, "conversationUuid", convUUID))
	payload.Write(appendV8TestString(nil, "name", "Synthetic 12 Turn Conversation"))
	payload.Write(appendV8TestString(nil, "model", "claude-sonnet-5-5"))
	payload.Write(appendV8TestString(nil, "current_leaf_message_uuid", leafUUID))
	payload.WriteString("chat_messages")

	// Build 12 turns (6 human, 6 assistant) linked in sequence
	parentUUID := "00000000-0000-4000-8000-000000000000"
	for i := 1; i <= 12; i++ {
		msgUUID := fmt.Sprintf("11111111-0000-0000-0000-%012d", i)
		sender := "human"
		text := fmt.Sprintf("Question %d with Latin-1 byte \xa7 and caf\xe9", i)
		if i%2 == 0 {
			sender = "assistant"
			text = fmt.Sprintf("Answer %d explaining section \xa7%d.1", i, i)
		}
		payload.Write(buildSyntheticMessage(msgUUID, parentUUID, sender, text))
		parentUUID = msgUUID
	}

	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(filepath.Join(blobDir, "synthetic_12_blob"), fileData.Bytes(), 0644)

	detail, err := GetConversationDetail(tempDir, convUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed: %v", err)
	}

	if len(detail.Messages) != 12 {
		t.Fatalf("expected 12 messages, got %d", len(detail.Messages))
	}

	humanCount := 0
	assistantCount := 0
	for i, m := range detail.Messages {
		if !utf8.ValidString(m.Text) {
			t.Errorf("message %d text is not valid UTF-8: %q", i, m.Text)
		}
		if m.Sender == "human" {
			humanCount++
		} else if m.Sender == "assistant" {
			assistantCount++
		}
	}

	if humanCount != 6 || assistantCount != 6 {
		t.Errorf("expected 6 human and 6 assistant, got %d human, %d assistant", humanCount, assistantCount)
	}

	if detail.Summary.CurrentLeaf != leafUUID {
		t.Errorf("expected leaf %s, got %s", leafUUID, detail.Summary.CurrentLeaf)
	}
	if detail.Messages[len(detail.Messages)-1].UUID != leafUUID {
		t.Errorf("last message UUID %s does not match leaf %s", detail.Messages[len(detail.Messages)-1].UUID, leafUUID)
	}
}

func TestRealProfileValidation(t *testing.T) {
	// Opt-in integration test via environment variable
	profilePath := os.Getenv("CLAUDE_SWITCH_TEST_PROFILE")
	targetUUID := os.Getenv("CLAUDE_SWITCH_TEST_UUID")
	if profilePath == "" || targetUUID == "" {
		t.Skip("skipping real profile integration test; set CLAUDE_SWITCH_TEST_PROFILE and CLAUDE_SWITCH_TEST_UUID to enable")
	}

	detail, err := GetConversationDetail(profilePath, targetUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed: %v", err)
	}

	if len(detail.Messages) == 0 {
		t.Fatalf("expected messages, got 0")
	}

	for i, m := range detail.Messages {
		if !utf8.ValidString(m.Text) {
			t.Errorf("message %d (%s) text is not valid UTF-8", i, m.UUID)
		}
	}

	if len(detail.Attachments) != 2 {
		t.Errorf("expected 2 attachments in real profile, got %d", len(detail.Attachments))
	} else {
		t.Logf("Attachment 1: %s (type: %s, size: %d)", detail.Attachments[0].FileName, detail.Attachments[0].FileType, len(detail.Attachments[0].ExtractedContent))
		t.Logf("Attachment 2: %s (type: %s, size: %d)", detail.Attachments[1].FileName, detail.Attachments[1].FileType, len(detail.Attachments[1].ExtractedContent))
	}

	// Verify Message 10 does not contain raw serialized metadata
	if len(detail.Messages) >= 10 {
		msg10 := detail.Messages[9] // 0-indexed Message 10
		if strings.Contains(msg10.Text, "sync_sources") || strings.Contains(msg10.Text, "files") || strings.Contains(msg10.Text, "attachments") {
			t.Errorf("Message 10 contains raw serialized metadata: %q", msg10.Text)
		}
	}

	t.Logf("Validated real profile %s, conv %s: %d messages, %d attachments, leaf=%s (UTF-8 valid: true)",
		profilePath, targetUUID, len(detail.Messages), len(detail.Attachments), detail.Summary.CurrentLeaf)
}

func TestDenseAndSparseArraysWithLengthsMatchingTags(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	convUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	leafUUID := "01a13dd1-417a-7d7b-b856-acf6a3716ee3"
	msgUUID := "01a13ad7-b64b-758f-8fcf-aab77f6277c2"

	var payload bytes.Buffer
	payload.WriteByte('o')
	payload.Write(appendV8TestString(nil, "conversationUuid", convUUID))
	payload.Write(appendV8TestString(nil, "name", "Array Tags Matching Tag Values"))
	payload.Write(appendV8TestString(nil, "model", "claude-sonnet-5-5"))
	payload.Write(appendV8TestString(nil, "current_leaf_message_uuid", leafUUID))

	// 1. Dense array 'A' with length 34 (0x22 == '"') and trailer '$'
	payload.Write(appendV8TestString(nil, "dense_34", ""))
	payload.WriteByte('A')
	payload.WriteByte(34) // length 34 == '"'
	payload.Write(bytes.Repeat([]byte{'_'}, 34))
	payload.WriteByte('$') // End Dense Array
	payload.WriteByte(0)   // numProperties = 0
	payload.WriteByte(34)  // length = 34

	// 2. Dense array 'A' with length 111 (0x6f == 'o') and trailer '$'
	payload.Write(appendV8TestString(nil, "dense_111", ""))
	payload.WriteByte('A')
	payload.WriteByte(111)                        // length 111 == 'o'
	payload.Write(bytes.Repeat([]byte{'_'}, 111)) // 111 undefined elements
	payload.WriteByte('$')                        // End Dense Array
	payload.WriteByte(0)                          // numProperties = 0
	payload.WriteByte(111)                        // length = 111 == 'o'

	// 3. Sparse array 'a' with length 111 (0x6f == 'o') and trailer '@'
	payload.Write(appendV8TestString(nil, "sparse_111", ""))
	payload.WriteByte('a')
	payload.WriteByte(111) // length 111 == 'o'
	payload.WriteByte('@') // End Sparse Array
	payload.WriteByte(0)   // numProperties = 0
	payload.WriteByte(111) // length = 111 == 'o'

	// 4. Sparse array 'a' with length 34 (0x22 == '"') and trailer '@'
	payload.Write(appendV8TestString(nil, "sparse_34", ""))
	payload.WriteByte('a')
	payload.WriteByte(34) // length 34 == '"'
	payload.WriteByte('@') // End Sparse Array
	payload.WriteByte(0)   // numProperties = 0
	payload.WriteByte(34)  // length = 34 == '"'

	// chat_messages array containing the message
	payload.WriteString("chat_messages")
	payload.WriteByte('A')
	payload.WriteByte(1) // length 1
	payload.Write(buildSyntheticMessage(leafUUID, "00000000-0000-4000-8000-000000000000", "human", "Question with nested arrays"))
	payload.WriteByte('$')
	payload.WriteByte(0)
	payload.WriteByte(1)

	payload.WriteByte('{')
	payload.WriteByte(7) // property count

	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(filepath.Join(blobDir, "test_nested_arrays_blob"), fileData.Bytes(), 0644)

	detail, err := GetConversationDetail(tempDir, convUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed: %v", err)
	}

	if detail.Summary.Title != "Array Tags Matching Tag Values" {
		t.Errorf("expected Title %q, got %q", "Array Tags Matching Tag Values", detail.Summary.Title)
	}
	if detail.Summary.Model != "claude-sonnet-5-5" {
		t.Errorf("expected Model %q, got %q", "claude-sonnet-5-5", detail.Summary.Model)
	}
	if detail.Summary.CurrentLeaf != leafUUID {
		t.Errorf("expected Leaf %q, got %q", leafUUID, detail.Summary.CurrentLeaf)
	}
	if len(detail.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(detail.Messages))
	}
	if !strings.Contains(detail.Messages[0].Text, "Question with nested arrays") {
		t.Errorf("unexpected message text: %q", detail.Messages[0].Text)
	}
	_ = msgUUID
}

func TestConversationWithMoreThan111Messages(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	convUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	totalMessages := 115
	leafUUID := fmt.Sprintf("11111111-0000-0000-0000-%012d", totalMessages)

	var payload bytes.Buffer
	payload.WriteByte('o')
	payload.Write(appendV8TestString(nil, "conversationUuid", convUUID))
	payload.Write(appendV8TestString(nil, "name", "Long Conversation Over 111 Messages"))
	payload.Write(appendV8TestString(nil, "model", "claude-sonnet-5-5"))
	payload.Write(appendV8TestString(nil, "current_leaf_message_uuid", leafUUID))
	payload.WriteString("chat_messages")

	// Array tag 'A' (Dense JS Array) with length 115 (0x73)
	payload.WriteByte('A')
	payload.WriteByte(byte(totalMessages))

	parentUUID := "00000000-0000-4000-8000-000000000000"
	for i := 1; i <= totalMessages; i++ {
		msgUUID := fmt.Sprintf("11111111-0000-0000-0000-%012d", i)
		sender := "human"
		text := fmt.Sprintf("Turn %d question", i)
		if i%2 == 0 {
			sender = "assistant"
			text = fmt.Sprintf("Turn %d answer", i)
		}
		payload.Write(buildSyntheticMessage(msgUUID, parentUUID, sender, text))
		parentUUID = msgUUID
	}

	payload.WriteByte('$')
	payload.WriteByte(0)
	payload.WriteByte(byte(totalMessages))

	payload.WriteByte('{')
	payload.WriteByte(5)

	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(filepath.Join(blobDir, "test_115_messages_blob"), fileData.Bytes(), 0644)

	detail, err := GetConversationDetail(tempDir, convUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed for 115 messages conversation: %v", err)
	}

	if detail.Summary.Title != "Long Conversation Over 111 Messages" {
		t.Errorf("expected Title %q, got %q", "Long Conversation Over 111 Messages", detail.Summary.Title)
	}
	if detail.Summary.CurrentLeaf != leafUUID {
		t.Errorf("expected Leaf %q, got %q", leafUUID, detail.Summary.CurrentLeaf)
	}
	if len(detail.Messages) != totalMessages {
		t.Fatalf("expected %d messages, got %d", totalMessages, len(detail.Messages))
	}
	if detail.Messages[0].Text != "Turn 1 question" {
		t.Errorf("expected first message %q, got %q", "Turn 1 question", detail.Messages[0].Text)
	}
	if detail.Messages[totalMessages-1].Text != "Turn 115 question" {
		t.Errorf("expected last message %q, got %q", "Turn 115 question", detail.Messages[totalMessages-1].Text)
	}
}

func buildSyntheticAttachment(id, fileName, fileType, content string) []byte {
	var buf bytes.Buffer
	buf.WriteByte('o')
	buf.Write(appendV8TestString(nil, "id", id))
	buf.Write(appendV8TestString(nil, "file_name", fileName))
	buf.Write(appendV8TestString(nil, "file_type", fileType))
	buf.Write(appendV8TestString(nil, "extracted_content", content))
	buf.WriteByte('{')
	buf.WriteByte(4)
	return buf.Bytes()
}

func TestExtractAttachmentsFromBlob(t *testing.T) {
	tempDir := t.TempDir()
	blobDir := filepath.Join(tempDir, "IndexedDB", "https_claude.ai_0.indexeddb.blob", "1", "00")
	if err := os.MkdirAll(blobDir, 0755); err != nil {
		t.Fatalf("failed to create blob dir: %v", err)
	}

	convUUID := "842eb422-4a50-4e9a-a595-d45b4b5a877e"
	msg1UUID := "01a13ad7-b64b-758f-8fcf-aab77f6277c1"
	msg2UUID := "01a13ad7-b64b-758f-8fcf-aab77f6277c2"
	leafUUID := "01a13dd1-417a-7d7b-b856-acf6a3716ee3"
	att1ID := "a117f5c7-17f4-5ce7-a930-8b2a3135057d"
	att2ID := "ea2d8f15-fe1d-50e8-9018-2119abe689a2"

	var payload bytes.Buffer
	payload.WriteByte('o')
	payload.Write(appendV8TestString(nil, "conversationUuid", convUUID))
	payload.Write(appendV8TestString(nil, "name", "Attachment Test Conversation"))
	payload.Write(appendV8TestString(nil, "current_leaf_message_uuid", leafUUID))
	payload.Write(appendV8TestString(nil, "chat_messages", ""))
	payload.WriteByte('A')
	payload.WriteByte(3)

	// Message 1: First message (human) with sender THEN attachments (real Claude serialization order)
	var msg1Buf bytes.Buffer
	msg1Buf.WriteByte('o')
	msg1Buf.Write(appendV8TestString(nil, "uuid", msg1UUID))
	msg1Buf.Write(appendV8TestString(nil, "text", "Please review the first document"))
	msg1Buf.Write(appendV8TestString(nil, "sender", "human"))
	msg1Buf.Write(appendV8TestString(nil, "attachments", ""))
	msg1Buf.WriteByte('a')
	msg1Buf.WriteByte(1)
	msg1Buf.Write(buildSyntheticAttachment(att1ID, "", "txt", "# First Doc\nContent of first document"))
	msg1Buf.WriteByte('@')
	msg1Buf.WriteByte(0)
	msg1Buf.WriteByte(1)
	msg1Buf.Write(appendV8TestString(nil, "parent_message_uuid", "00000000-0000-4000-8000-000000000000"))
	msg1Buf.WriteByte('{')
	msg1Buf.WriteByte(5)
	payload.Write(msg1Buf.Bytes())

	// Message 2: Middle message (assistant) with sender and NO attachments
	var msg2Buf bytes.Buffer
	msg2Buf.WriteByte('o')
	msg2Buf.Write(appendV8TestString(nil, "uuid", msg2UUID))
	msg2Buf.Write(appendV8TestString(nil, "text", "Here is my preliminary review"))
	msg2Buf.Write(appendV8TestString(nil, "sender", "assistant"))
	msg2Buf.Write(appendV8TestString(nil, "parent_message_uuid", msg1UUID))
	msg2Buf.WriteByte('{')
	msg2Buf.WriteByte(4)
	payload.Write(msg2Buf.Bytes())

	// Message 3: Last message (human, leaf) with sender THEN attachments
	var msg3Buf bytes.Buffer
	msg3Buf.WriteByte('o')
	msg3Buf.Write(appendV8TestString(nil, "uuid", leafUUID))
	msg3Buf.Write(appendV8TestString(nil, "text", "Here is the final plan"))
	msg3Buf.Write(appendV8TestString(nil, "sender", "human"))
	msg3Buf.Write(appendV8TestString(nil, "attachments", ""))
	msg3Buf.WriteByte('a')
	msg3Buf.WriteByte(1)
	msg3Buf.Write(buildSyntheticAttachment(att2ID, "plan.txt", "txt", "Content of final plan"))
	msg3Buf.WriteByte('@')
	msg3Buf.WriteByte(0)
	msg3Buf.WriteByte(1)
	msg3Buf.Write(appendV8TestString(nil, "parent_message_uuid", msg2UUID))
	msg3Buf.WriteByte('{')
	msg3Buf.WriteByte(5)
	payload.Write(msg3Buf.Bytes())

	payload.WriteByte('$')
	payload.WriteByte(0)
	payload.WriteByte(3)

	payload.WriteByte('{')
	payload.WriteByte(4)

	var fileData bytes.Buffer
	fileData.Write([]byte{0xff, 0x11, 0x02})
	fileData.Write(makeSnappyLiteral(payload.Bytes()))
	_ = os.WriteFile(filepath.Join(blobDir, "test_attachments_blob"), fileData.Bytes(), 0644)

	detail, err := GetConversationDetail(tempDir, convUUID)
	if err != nil {
		t.Fatalf("GetConversationDetail failed: %v", err)
	}

	if len(detail.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(detail.Messages))
	}

	// Verify Message 1 (first message) received Attachment 1
	if len(detail.Messages[0].Attachments) != 1 {
		t.Fatalf("expected msg0 to have 1 attachment, got %d", len(detail.Messages[0].Attachments))
	}
	if detail.Messages[0].Attachments[0].ID != att1ID {
		t.Errorf("msg0 attachment ID mismatch: got %s, want %s", detail.Messages[0].Attachments[0].ID, att1ID)
	}
	if detail.Messages[0].Attachments[0].FileName != "First Doc" {
		t.Errorf("msg0 attachment FileName mismatch: got %q, want 'First Doc'", detail.Messages[0].Attachments[0].FileName)
	}

	// Verify Message 2 has 0 attachments
	if len(detail.Messages[1].Attachments) != 0 {
		t.Errorf("expected msg1 to have 0 attachments, got %d", len(detail.Messages[1].Attachments))
	}

	// Verify Message 3 (last message) received Attachment 2
	if len(detail.Messages[2].Attachments) != 1 {
		t.Fatalf("expected msg2 (last message) to have 1 attachment, got %d", len(detail.Messages[2].Attachments))
	}
	if detail.Messages[2].Attachments[0].ID != att2ID {
		t.Errorf("msg2 attachment ID mismatch: got %s, want %s", detail.Messages[2].Attachments[0].ID, att2ID)
	}
	if detail.Messages[2].Attachments[0].FileName != "plan.txt" {
		t.Errorf("msg2 attachment FileName mismatch: got %q, want 'plan.txt'", detail.Messages[2].Attachments[0].FileName)
	}

	// Verify ConversationDetail aggregated both attachments without duplication
	if len(detail.Attachments) != 2 {
		t.Fatalf("expected 2 aggregated attachments in detail, got %d", len(detail.Attachments))
	}
}
