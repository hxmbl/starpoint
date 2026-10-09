// Package tracking provides execution history tracking for Starpoint.
//
// It records where software packages have been executed, enabling queries like
// "where was this package last run?" and "what packages have been run in this
// directory?". The tracking data is persisted to a JSON file at
// ~/.starpoint/runs.json.
//
// The Tracker is thread-safe and can be used concurrently from multiple goroutines.
package tracking
