package extension

import "time"

// ListWaitOutcome describes why an admitted change wait ended. It carries no
// principal, path, token or space identifier.
type ListWaitOutcome string

const (
	ListWaitChanged   ListWaitOutcome = "changed"
	ListWaitTimeout   ListWaitOutcome = "timeout"
	ListWaitCancelled ListWaitOutcome = "cancelled"
	ListWaitStopped   ListWaitOutcome = "stopped"
	ListWaitFailed    ListWaitOutcome = "probe_failed"
)

// ListWaitObserver is optional trusted application instrumentation. Methods may
// run concurrently and must return promptly without I/O or calls back into Core.
// Each Started has exactly one Finished, including HEAD probes that end early.
// Elapsed covers admission through release, including the HEAD probe. Saturated
// requests receive retry advice and never call Started/Finished. Authentication
// and authorization happen before this mechanism. Nil adds no instrumentation.
type ListWaitObserver interface {
	ListWaitStarted()
	ListWaitFinished(ListWaitOutcome, time.Duration)
	ListWaitSaturated()
}
