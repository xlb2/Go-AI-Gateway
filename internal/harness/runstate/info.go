// Package runstate defines the active-run snapshot shared by execution and transports.
package runstate

import "time"

// Info identifies one active operation in the user's sole legacy session.
type Info struct {
	ID              string    `json:"run_id"`
	UserID          uint      `json:"user_id"`
	SessionID       string    `json:"session_id"`
	Kind            string    `json:"kind"`
	StartedAt       time.Time `json:"started_at"`
	CancelRequested bool      `json:"cancel_requested"`
}
