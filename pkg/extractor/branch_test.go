package extractor

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func buildSyntheticMessage(uuid, parentUUID, sender, text string) []byte {
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf(`uuid"$%s`, uuid))
	buf.WriteString("type\"\x04text")
	buf.WriteString("\x04text\"")
	buf.WriteByte(byte(len(text)))
	buf.WriteString(text)
	if sender == "human" {
		buf.WriteString("sender\"\x05human")
	} else {
		buf.WriteString("sender\"	assistant")
	}
	if parentUUID != "" {
		buf.WriteString(fmt.Sprintf(`parent_message_uuid"$%s`, parentUUID))
	}
	return buf.Bytes()
}

func TestExtractMessagesWithBranchProduction(t *testing.T) {
	rootUUID := "00000001-0000-0000-0000-000000000001"
	parent0 := "00000000-0000-4000-8000-000000000000"
	turn1UUID := "00000002-0000-0000-0000-000000000002"
	branchAUUID := "00000003-0000-0000-0000-000000000003"
	branchBUUID := "00000004-0000-0000-0000-000000000004"

	// Construct message stream: Root -> Turn1 -> BranchA (abandoned), and BranchB (active leaf)
	var stream bytes.Buffer
	stream.Write(buildSyntheticMessage(rootUUID, parent0, "human", "Root question"))
	stream.Write(buildSyntheticMessage(turn1UUID, rootUUID, "assistant", "Answer 1"))
	stream.Write(buildSyntheticMessage(branchAUUID, turn1UUID, "human", "Abandoned edit question"))
	stream.Write(buildSyntheticMessage(branchBUUID, turn1UUID, "human", "Active branch question"))

	// 1. Call production extractMessagesWithBranch for active leaf (branchB)
	activeBranch, err := extractMessagesWithBranch(stream.Bytes(), branchBUUID)
	if err != nil {
		t.Fatalf("extractMessagesWithBranch failed: %v", err)
	}

	if len(activeBranch) != 3 {
		t.Fatalf("expected 3 messages in active branch, got %d", len(activeBranch))
	}

	expectedUUIDs := []string{rootUUID, turn1UUID, branchBUUID}
	for i, expected := range expectedUUIDs {
		if activeBranch[i].UUID != expected {
			t.Errorf("at index %d: expected UUID %s, got %s", i, expected, activeBranch[i].UUID)
		}
	}

	// Verify abandoned branch A is excluded
	for _, m := range activeBranch {
		if m.UUID == branchAUUID {
			t.Errorf("abandoned branch A (%s) found in active branch", branchAUUID)
		}
	}

	// 2. Test Truncated / Broken branch: leaf points to missing parent
	brokenLeafUUID := "00000005-0000-0000-0000-000000000005"
	missingParentUUID := "00000099-0000-0000-0000-000000000099"
	var brokenStream bytes.Buffer
	brokenStream.Write(buildSyntheticMessage(brokenLeafUUID, missingParentUUID, "assistant", "Orphaned leaf message"))

	_, errBroken := extractMessagesWithBranch(brokenStream.Bytes(), brokenLeafUUID)
	if errBroken == nil {
		t.Fatalf("expected error for broken branch with missing parent, but got nil")
	}
	if !strings.Contains(errBroken.Error(), "nhánh hội thoại bị gián đoạn") {
		t.Errorf("unexpected error message: %v", errBroken)
	}

	// 3. Test empty parent UUID on non-root message: must NOT be silently treated as root
	emptyParentLeafUUID := "00000006-0000-0000-0000-000000000006"
	var emptyParentStream bytes.Buffer
	// Message has no parentUUID, so ParentUUID will be ""
	emptyParentStream.Write(buildSyntheticMessage(emptyParentLeafUUID, "", "assistant", "Message with empty parent"))

	_, errEmpty := extractMessagesWithBranch(emptyParentStream.Bytes(), emptyParentLeafUUID)
	if errEmpty == nil {
		t.Fatalf("expected error for message with empty parent UUID, but got nil")
	}
	if !strings.Contains(errEmpty.Error(), "ParentUUID rỗng") {
		t.Errorf("unexpected error message: %v", errEmpty)
	}
}
