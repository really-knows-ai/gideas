package flow

import (
	"context"
	"fmt"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// Law is a domain object wrapping a governance law from the Librarian.
type Law struct {
	pb        *flowv1.Law
	librarian flowv1.LibrarianServiceClient
}

// newLaw creates a Law domain object wrapping a proto Law.
func newLaw(pb *flowv1.Law, librarian flowv1.LibrarianServiceClient) *Law {
	return &Law{pb: pb, librarian: librarian}
}

// ID returns the law identifier from the proto.
func (l *Law) ID() string {
	if l.pb == nil {
		return ""
	}
	return l.pb.GetId()
}

// GetGoal returns the goal of the law from the proto.
func (l *Law) GetGoal() string {
	if l.pb == nil {
		return ""
	}
	return l.pb.GetGoal()
}

// GetTier returns the law tier as an int32 from the proto LawTier enum.
func (l *Law) GetTier() int32 {
	if l.pb == nil {
		return 0
	}
	return int32(l.pb.GetTier())
}

// GetGroup returns the group name from the proto.
func (l *Law) GetGroup() string {
	if l.pb == nil {
		return ""
	}
	return l.pb.GetGroup()
}

// GetRepresentations returns the representation list from the proto.
func (l *Law) GetRepresentations() []*flowv1.Representation {
	if l.pb == nil {
		return nil
	}
	return l.pb.GetRepresentations()
}

// PB returns the underlying proto Law pointer. Returns nil if the proto is nil.
func (l *Law) PB() *flowv1.Law {
	return l.pb
}

// Cite records usage of this law via the Librarian Cite RPC.
func (l *Law) Cite() error {
	_, err := l.librarian.Cite(context.Background(), &flowv1.CiteRequest{
		LawIds: []string{l.ID()},
	})
	if err != nil {
		return fmt.Errorf("flow sdk: cite law %q: %w", l.ID(), err)
	}
	return nil
}

// Attest stamps "law-<id>-<type>" on the given artefact.
// The repType MIME type has "/" replaced with "-" to produce a valid stamp name.
func (l *Law) Attest(artefact *Artefact, repType string) error {
	stampName := "law-" + l.ID() + "-" + strings.ReplaceAll(repType, "/", "-")
	return artefact.Stamp(stampName)
}
