// Package skills holds the action kinds a chat turn may propose. The model
// never invents a new kind of button. Each opens something: a change is made by
// a tool call the person approves, never by a button.
package skills

// Action kinds the adapter will forward to the UI.
const (
	ActionMetrics = "metrics"
	ActionStatus  = "status"
	ActionSupport = "support"
)
