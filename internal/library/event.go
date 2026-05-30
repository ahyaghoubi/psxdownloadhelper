// Package library indexes the local download folder and watches it for
// file changes. See plan.md §5.3 and §6.2. The partial-write debounce
// policy (size stable for N polls) is documented in plan.md §9 risk
// "Library watcher races a partial write".
package library

// EventKind describes a filesystem transition observed by the watcher.
type EventKind string

const (
	// KindCreated fires the first time a path is observed.
	KindCreated EventKind = "created"
	// KindWritten fires for each fsnotify write notification while the
	// file is still settling.
	KindWritten EventKind = "written"
	// KindStable fires when the file's size has been unchanged for the
	// configured settle window AND size > 0. This is the signal session.
	KindStable EventKind = "stable"
	// KindRemoved fires when a file disappears (delete or rename-out).
	KindRemoved EventKind = "removed"
)

// Event is emitted by Watcher.Events for each filesystem transition we care
// about. Path is always the absolute path on disk.
type Event struct {
	Path     string
	Basename string
	Size     int64
	Kind     EventKind
}
