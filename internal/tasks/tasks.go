// Package tasks defines shared task type constants used across the codebase.
// This ensures consistency between task enqueueing (handlers) and task processing (worker).
package tasks

import "time"

const (
	// TypeWorkerPing is used to verify the worker is alive and processing tasks.
	TypeWorkerPing = "worker:ping"
	TypeClickTrack = "click:tracking"
)

// PingTaskPayload is the payload for the worker ping task, including correlation ID.
type PingTaskPayload struct {
	Message   string    `json:"message"`
	RequestID string    `json:"request_id"`
	QueuedAt  time.Time `json:"queued_at"`
}

type ClickTrackPayload struct {
	Slug      string    `json:"slug"`
	IpAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	Referer   string    `json:"referer"`
	RequestID string    `json:"request_id"`
	ClickedAt time.Time `json:"clicked_at"`
}
