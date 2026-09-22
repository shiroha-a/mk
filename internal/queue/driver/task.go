// Package driver defines the queue driver abstraction. The concrete
// driver (mkq) implements these interfaces; the queue facade and
// processors depend only on this package.
package driver

// Task represents a queueable unit of work, decoupled from any
// specific driver. Drivers wrap their native task representation in
// an adapter that implements this interface.
type Task interface {
	Type() string
	Payload() []byte
}

// RawTask is a minimal Task implementation useful for tests and for
// constructing tasks at API boundaries where no driver is involved.
type RawTask struct {
	TypeName string
	Body     []byte
}

// Type returns the task type identifier.
func (t RawTask) Type() string { return t.TypeName }

// Payload returns the task payload bytes.
func (t RawTask) Payload() []byte { return t.Body }
