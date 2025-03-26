package model

import "time"

// State is the lifecycle state of a task.
type State string

const (
	StateStopped      State = "stopped"
	StateStarting     State = "starting"
	StateSnapshotting State = "snapshotting"
	StateStreaming    State = "streaming"
	StatePaused       State = "paused"
	StateResyncing    State = "resyncing"
	StateRetrying     State = "retrying"
	StateFailed       State = "failed"
	// StateHeld means a safety guard stopped the task; it needs an operator.
	StateHeld      State = "held"
	StateCompleted State = "completed"
	StateRunning   State = "running" // desired state only
)

// Active reports whether the state belongs to a running pipeline.
func (s State) Active() bool {
	switch s {
	case StateStarting, StateSnapshotting, StateStreaming, StateResyncing, StateRetrying:
		return true
	}
	return false
}

// Counters are cumulative task statistics.
type Counters struct {
	Upserted  int64 `json:"upserted"`
	Deleted   int64 `json:"deleted"`
	Events    int64 `json:"events"`
	Batches   int64 `json:"batches"`
	Errors    int64 `json:"errors"`
	Retries   int64 `json:"retries"`
	DLQ       int64 `json:"dlq"`
	Coalesced int64 `json:"coalesced"`
}

// Progress describes snapshot progress.
type Progress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// Status is the runtime view of a task.
type Status struct {
	TaskID      string    `json:"taskId"`
	State       State     `json:"state"`
	Message     string    `json:"message,omitempty"`
	Error       string    `json:"error,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
	LastEventAt time.Time `json:"lastEventAt,omitempty"`
	// LastSyncAt is when Meilisearch last confirmed a batch of this task.
	LastSyncAt time.Time `json:"lastSyncAt,omitzero"`
	// PendingSince is when the oldest change received after the last
	// confirmation arrived (zero when everything received is confirmed).
	// It lets lag grow while Meilisearch confirms nothing.
	PendingSince time.Time `json:"pendingSince,omitzero"`
	// LagMs is the delay between a change in the source and its confirmation in Meilisearch.
	LagMs      int64    `json:"lagMs"`
	Rate       float64  `json:"rate"` // documents per second, smoothed
	Checkpoint string   `json:"checkpoint,omitempty"`
	Snapshot   Progress `json:"snapshot"`
	Counters   Counters `json:"counters"`
	Attempt    int      `json:"attempt,omitempty"`
	Inflight   int      `json:"inflight"`
}

// TaskView is a task with its status, as returned by the API.
type TaskView struct {
	Task
	Status Status `json:"status"`
}

// DLQEntry is a document Meilisearch rejected.
type DLQEntry struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"taskId"`
	DocID     string         `json:"docId"`
	Op        string         `json:"op"`
	Document  map[string]any `json:"document,omitempty"`
	Error     string         `json:"error"`
	Code      string         `json:"code,omitempty"`
	Attempts  int            `json:"attempts"`
	CreatedAt time.Time      `json:"createdAt"`
}

// LogEntry is a task activity record shown in the UI.
type LogEntry struct {
	Time    time.Time `json:"time"`
	TaskID  string    `json:"taskId,omitempty"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}
