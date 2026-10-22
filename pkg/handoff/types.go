package handoff

type HandoffStatus string

const (
	StatusSuccess            HandoffStatus = "success"
	StatusSkipped            HandoffStatus = "skipped"
	StatusExtractionFailed   HandoffStatus = "extraction_failed"
	StatusWriteFailed        HandoffStatus = "write_failed"
	StatusVerificationFailed HandoffStatus = "verification_failed"
	StatusClipboardFailed    HandoffStatus = "clipboard_failed"
)

type HandoffResult struct {
	Status       HandoffStatus `json:"status"`
	SnapshotPath string        `json:"snapshot_path,omitempty"`
	Title        string        `json:"title,omitempty"`
	Error        string        `json:"error,omitempty"`
	Directive    string        `json:"directive,omitempty"`
}

func (r *HandoffResult) Succeeded() bool {
	return r.Status == StatusSuccess || r.Status == StatusClipboardFailed
}
