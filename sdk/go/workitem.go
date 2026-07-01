package flow

// Workitem is a domain object representing a workitem in a Foundry Flow.
// It carries a reference to the session for making gRPC calls.
type Workitem struct {
	session *session
	id      string
}

// ID returns the workitem identifier.
func (w *Workitem) ID() string {
	return w.id
}
