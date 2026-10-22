package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"claude-switch/pkg/config"
)

type SwitchJournalStep string

const (
	StepStarted        SwitchJournalStep = "started"
	StepClaudeStopped  SwitchJournalStep = "claude_stopped"
	StepHandoffDone    SwitchJournalStep = "handoff_done"
	StepSymlinkUpdated SwitchJournalStep = "symlink_updated"
	StepClaudeLaunched SwitchJournalStep = "claude_launched"
	StepVerified       SwitchJournalStep = "verified"
)

type SwitchJournal struct {
	OperationID string            `json:"operation_id"`
	Timestamp   string            `json:"timestamp"`
	Source      string            `json:"source"`
	Target      string            `json:"target"`
	HandoffPath string            `json:"handoff_path,omitempty"`
	Step        SwitchJournalStep `json:"step"`
}

func GetJournalPath() string {
	return filepath.Join(config.GetBaseDir(), "journal.json")
}

func RecordJournalStep(id, source, target, handoffPath string, step SwitchJournalStep) error {
	j := SwitchJournal{
		OperationID: id,
		Timestamp:   time.Now().Format(time.RFC3339),
		Source:      source,
		Target:      target,
		HandoffPath: handoffPath,
		Step:        step,
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(GetJournalPath(), data, 0600)
}

func ClearJournal() {
	_ = os.Remove(GetJournalPath())
}

func ReadJournal() (*SwitchJournal, error) {
	data, err := os.ReadFile(GetJournalPath())
	if err != nil {
		return nil, err
	}
	var j SwitchJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	return &j, nil
}
