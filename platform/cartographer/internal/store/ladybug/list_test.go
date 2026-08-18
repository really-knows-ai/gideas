package ladybug

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
)

func TestListEntities_DefaultPageSize(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	for i := range 5 {
		_, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
	}

	// pageSize=0 should default to 1000 (more than enough for 5 entities).
	entities, token, err := s.ListEntities(context.Background(), "Component", 0, "", "")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) != 5 {
		t.Fatalf("expected 5 entities, got %d", len(entities))
	}
	if token != "" {
		t.Errorf("expected empty token, got %q", token)
	}
}

func TestListEntities_PageSizeCap(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, _, err = s.ListEntities(context.Background(), "Component", 1001, "", "")
	if err == nil {
		t.Fatal("expected error for page size > 1000")
	}
	if !errors.Is(err, store.ErrInvalidPageSize) {
		t.Errorf("expected ErrInvalidPageSize, got %v", err)
	}
}

func TestListEntities_NegativePageSize(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, _, err = s.ListEntities(context.Background(), "Component", -1, "", "")
	if err == nil {
		t.Fatal("expected error for negative page size")
	}
	if !errors.Is(err, store.ErrInvalidPageSize) {
		t.Errorf("expected ErrInvalidPageSize, got %v", err)
	}
}

func TestListEntities_Pagination(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	const total = 5
	const pageSize = 2
	ids := make([]string, total)
	for i := range total {
		e, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
		if err != nil {
			t.Fatalf("CreateEntity %d: %v", i, err)
		}
		ids[i] = e.Id
	}

	var all []string
	token := ""
	for {
		entities, nextToken, err := s.ListEntities(context.Background(), "Component", pageSize, token, "")
		if err != nil {
			t.Fatalf("ListEntities: %v", err)
		}
		for _, e := range entities {
			all = append(all, e.Id)
		}
		if nextToken == "" {
			break
		}
		token = nextToken
	}

	if len(all) != total {
		t.Fatalf("expected %d total entities via pagination, got %d", total, len(all))
	}
}

// TestListEntities_PageTokenOverflowBoundary pins the offset pagination boundary that
// the query.go:ListEntities ponytail documents: any non-negative int64 page token is
// accepted, and `offset + pageSize` can overflow to a negative next-token value that the
// follow-up call rejects as ErrInvalidPageToken. With a real graph too small to reach
// such an offset no next token is emitted (SKIP past the rows yields nothing), which is
// exactly why the overflow is practically unreachable — the boundary test asserts the
// accepted-bound and the rejected-downstream-bound so the ceiling stays explicit.
func TestListEntities_PageTokenOverflowBoundary(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	const pageSize = 10

	// A non-negative offset at the int64 limit is parsed and accepted (not
	// ErrInvalidPageToken). On a small graph the SKIP exhausts the rows, so no
	// next token is emitted — no overflow, no error.
	maxOffset := math.MaxInt64
	maxTok := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d", int64(maxOffset)))
	entities, nextTok, err := s.ListEntities(ctx, "Component", pageSize, maxTok, "")
	if err != nil {
		t.Fatalf("largest accepted offset should not error, got %v", err)
	}
	if len(entities) != 0 || nextTok != "" {
		t.Fatalf("expected empty page and no next token at max offset, got entities=%d nextToken=%q", len(entities), nextTok)
	}

	// That same offset plus pageSize is what the next-token computation would
	// produce; it overflows to a negative value. Feed it back in as a token, as
	// the ponytail's failure mode describes, and the follow-up call rejects it.
	overflowed := int64(maxOffset) + int64(pageSize)
	if overflowed >= 0 {
		t.Fatalf("overflow guard ineffective: offset %d + pagesize %d did not overflow", maxOffset, pageSize)
	}
	overflowTok := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d", overflowed))
	_, _, err = s.ListEntities(ctx, "Component", pageSize, overflowTok, "")
	if !errors.Is(err, store.ErrInvalidPageToken) {
		t.Errorf("overflowed negative token should be rejected as ErrInvalidPageToken, got %v", err)
	}
}

func TestListEntities_InvalidPageToken(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	for _, tok := range []string{
		"not-base64!!!",
		base64.StdEncoding.EncodeToString([]byte("not-a-number")),
		base64.StdEncoding.EncodeToString([]byte("-5")),
	} {
		_, _, err = s.ListEntities(context.Background(), "Component", 10, tok, "")
		if err == nil {
			t.Fatalf("expected error for malformed page token %q", tok)
		}
		if !errors.Is(err, store.ErrInvalidPageToken) {
			t.Errorf("token %q: expected ErrInvalidPageToken, got %v", tok, err)
		}
	}
}

func TestListEntities_UnknownType(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, _, err = s.ListEntities(context.Background(), "NoSuchType", 10, "", "")
	if err == nil {
		t.Fatal("expected error for unknown entity type")
	}
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType, got %v", err)
	}
}

// TestListEntities_CheckOrder pins the SPEC:960 ListEntities structural check
// order — unknown entity type → pageSize → pageToken — at the store layer: when
// multiple inputs are invalid, the earliest check in that order is the error
// surfaced (entity type wins over pageSize and pageToken; pageSize wins over
// pageToken).
func TestListEntities_CheckOrder(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	badTok := base64.StdEncoding.EncodeToString([]byte("not-a-number"))

	// Unknown entity type surfaces before invalid pageSize (negative and over-max).
	for _, pageSize := range []int{-1, 1001} {
		_, _, err := s.ListEntities(context.Background(), "NoSuchType", pageSize, "", "")
		if !errors.Is(err, store.ErrUnknownEntityType) {
			t.Errorf("pageSize %d: expected ErrUnknownEntityType, got %v", pageSize, err)
		}
	}

	// Unknown entity type surfaces before an invalid page token.
	_, _, err = s.ListEntities(context.Background(), "NoSuchType", 10, badTok, "")
	if !errors.Is(err, store.ErrUnknownEntityType) {
		t.Errorf("expected ErrUnknownEntityType over ErrInvalidPageToken, got %v", err)
	}

	// Invalid pageSize surfaces before an invalid page token (known type).
	_, _, err = s.ListEntities(context.Background(), "Component", -1, badTok, "")
	if !errors.Is(err, store.ErrInvalidPageSize) {
		t.Errorf("expected ErrInvalidPageSize over ErrInvalidPageToken, got %v", err)
	}
}
