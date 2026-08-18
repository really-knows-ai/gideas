package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestOpenClose(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatalf("openInMemory() error: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error: %v", err)
		}
	}()

	if s == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestOpenFileBacked(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", dir, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
}

func TestHealth(t *testing.T) {
	t.Run("in-memory", func(t *testing.T) {
		s, err := openInMemory()
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)

		health, err := s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health: %v", err)
		}

		if !health.LadybugOK {
			t.Error("expected LadybugOK to be true")
		}
		if health.SchemaApplied {
			t.Error("expected SchemaApplied to be false for fresh DB")
		}
		if !health.PVCWritable {
			t.Error("expected PVCWritable to be true for in-memory DB")
		}

		// Apply a schema, then health should report SchemaApplied.
		sch := &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{
				{Name: "Doc", Properties: []*flowv1.Property{
					{Name: "title", Type: "string"},
				}},
			},
		}
		if err := s.ApplySchema(context.Background(), sch); err != nil {
			t.Fatal(err)
		}

		health, err = s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health after schema: %v", err)
		}
		if !health.SchemaApplied {
			t.Error("expected SchemaApplied to be true after schema apply")
		}
	})

	t.Run("file-backed PVC writable", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)

		health, err := s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		if !health.LadybugOK {
			t.Error("expected LadybugOK to be true for file-backed DB")
		}
		if !health.PVCWritable {
			t.Error("expected PVCWritable to be true for writable temp dir")
		}
	})

	t.Run("closed store reports unhealthy", func(t *testing.T) {
		s, err := openInMemory()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		health, err := s.Health(context.Background())
		if err != nil {
			t.Fatalf("Health: %v", err)
		}
		if health.LadybugOK {
			t.Error("expected LadybugOK to be false for closed store")
		}
	})
}

func TestClosedStore_ReturnsError(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	err = s.ApplySchema(context.Background(), &flowv1.Schema{})
	if err == nil {
		t.Error("expected error when applying schema on closed store")
	}
}

func TestListMainEntityTypes(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	types, err := s.ListMainEntityTypes()
	if err != nil {
		t.Fatalf("ListMainEntityTypes: %v", err)
	}
	if len(types) != 0 {
		t.Errorf("expected empty list, got %v", types)
	}

	sch := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Book",
				Properties: []*flowv1.Property{
					{Name: "isbn", Type: "string"},
				},
			},
			{
				Name: "Author",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := s.ApplySchema(context.Background(), sch); err != nil {
		t.Fatal(err)
	}

	types, err = s.ListMainEntityTypes()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d: %v", len(types), types)
	}
	// Should be sorted.
	if types[0] != "Author" || types[1] != "Book" {
		t.Errorf("expected sorted [Author, Book], got %v", types)
	}
}

// Learnings rule "Sentinel errors over zero-value returns": a failed store must
// surface ErrDatabaseNotReady from ListMainEntityTypes rather than silently
// reporting an empty type list with a nil error.
func TestListMainEntityTypes_FailedStoreReturnsSentinel(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	db := s.(*ladybugDB)
	db.failed = true

	types, err := s.ListMainEntityTypes()
	if !errors.Is(err, store.ErrDatabaseNotReady) {
		t.Fatalf("expected ErrDatabaseNotReady for failed store, got %v", err)
	}
	if types != nil {
		t.Fatalf("expected nil types for failed store, got %v", types)
	}
}
