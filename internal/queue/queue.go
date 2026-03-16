// Package queue defines shared queue names and configuration used across the codebase.
// This ensures consistency between worker configuration and handler responses.
package queue

const (
	// QueueCritical is the highest priority queue.
	QueueCritical = "critical"
	// QueueDefault is the standard priority queue.
	QueueDefault = "default"
	// QueueLow is the lowest priority queue.
	QueueLow = "low"
)

// Names returns all queue names in priority order (highest to lowest).
func Names() []string {
	return []string{QueueCritical, QueueDefault, QueueLow}
}

// Priorities returns the queue priority configuration map.
// Higher values indicate higher priority.
func Priorities() map[string]int {
	return map[string]int{
		QueueCritical: 6,
		QueueDefault:  3,
		QueueLow:      1,
	}
}
