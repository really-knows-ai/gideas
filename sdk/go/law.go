package flow

import flowv1 "github.com/gideas/flow/gen/flow/v1"

// Law is a domain object wrapping a protobuf Law.
type Law struct {
	session *session
	pb      *flowv1.Law
}

// ID returns the law identifier.
func (l *Law) ID() string {
	return l.pb.GetId()
}

// GetGoal returns the goal of the law.
func (l *Law) GetGoal() string {
	return l.pb.GetGoal()
}
