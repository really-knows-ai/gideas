package ladybug

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// The corruption heuristic (ladybug.go corruptionCandidates, SPEC R8) classifies
// an OpenDatabase failure by file accessibility: a present-but-readable file is
// a corruption candidate (the engine could not parse genuine contents) while a
// file the OS layer cannot open is an operational (permission/I/O) failure that
// must NOT be treated as corrupt (removing it would destroy never-corrupt data).
// This unit test drives both outcomes.
func TestCorruptionCandidates_ReadableVersusUnreadable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "main.lbug")
	if err := os.WriteFile(dbPath, []byte("corrupt-bytes"), 0600); err != nil {
		t.Fatalf("write readable file: %v", err)
	}

	// Readable file -> candidate for corruption recovery.
	if !corruptionCandidates(dbPath) {
		t.Fatal("expected a readable present file to be a corruption candidate")
	}

	// Unreadable file (mode 000) -> NOT a candidate; it is an operational
	// open problem, not engine-unparseable content.
	if err := os.Chmod(dbPath, 0000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	if corruptionCandidates(dbPath) {
		t.Fatal("expected an unreadable file to NOT be a corruption candidate")
	}
	// Restore permissions so the temp dir can be cleaned up.
	if err := os.Chmod(dbPath, 0600); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}

	// A missing file is never a candidate (OpenDatabase creates it fresh).
	if corruptionCandidates(filepath.Join(dir, "absent.lbug")) {
		t.Fatal("expected an absent file to NOT be a corruption candidate")
	}
}

// Open's SPEC R8 repair path: a genuinely corrupted main.lbug (present and
// readable, but unparsable by the engine) is deleted and re-opened fresh, with
// schema rehydrated from the persisted metadata. An unreadable main.lbug is an
// operational failure and must NOT be deleted — Open fails and the file remains.
func TestOpenCorruptDatabase_RecoversOrFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	t.Run("readable corrupt file recovers", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
			Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
		}}}
		if err := s.ApplySchema(context.Background(), schema); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		dbPath := filepath.Join(dir, "main.lbug")
		// Overwrite with garbage the engine cannot parse.
		if err := os.WriteFile(dbPath, []byte("not a ladybug database"), 0600); err != nil {
			t.Fatalf("corrupt main.lbug: %v", err)
		}
		if !corruptionCandidates(dbPath) {
			t.Fatal("expected corrupt file to be classified as a corruption candidate")
		}
		recovered, err := Open(dir)
		if err != nil {
			t.Fatalf("Open should recover a corrupt readable main.lbug, got %v", err)
		}
		defer closeStore(t, recovered)
		// Recovery re-creates the schema from metadata.
		if !recovered.TableExists("Component") {
			t.Fatal("recovery did not rehydrate the Component table from metadata")
		}
		// The corrupt file was replaced by a freshly-created valid database.
		if _, err := os.Stat(dbPath); err != nil {
			t.Fatalf("recovered database file missing: %v", err)
		}
	})

	t.Run("unreadable file classified and preserved", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		dbPath := filepath.Join(dir, "main.lbug")
		if err := os.Chmod(dbPath, 0000); err != nil {
			t.Fatalf("chmod 000: %v", err)
		}
		// Not a corruption candidate: Open must fail WITHOUT removing the file.
		if corruptionCandidates(dbPath) {
			t.Fatal("unreadable file must not be a corruption candidate")
		}
		if reopened, err := Open(dir); err == nil {
			_ = reopened.Close()
			t.Fatal("expected Open to fail for an unreadable (non-corrupt) main.lbug")
		}
		if _, statErr := os.Stat(dbPath); statErr != nil {
			t.Fatalf("unreadable file was removed by Open: %v", statErr)
		}
	})
}

// The engine's write-ahead-log companions (<db>.lbug.wal and
// <db>.lbug.wal.checkpoint) are the artifacts a crash tears alongside the main
// file, and they can be the sole cause of the open failure: OpenDatabase
// replays a present WAL, so a torn WAL fails the open while main.lbug stays
// OS-readable and corruptionCandidates(main.lbug) reports corruption. SPEC R8
// recovery must therefore remove the WAL companions together with main.lbug,
// or the fresh re-open replays the still-torn WAL and fails again — a permanent
// crash loop that never reaches the R8 git re-hydration. Both subtests pin the
// companion removal (they fail pre-fix, which removed only main.lbug and left
// the torn WAL to fail the re-open).
func TestOpenCorruptDatabase_RemovesTornWalCompanions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	// setupDB opens a file-backed store, applies a schema, and closes it,
	// leaving a clean main.lbug + schema.json with no WAL residue.
	setupDB := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
			Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
		}}}
		if err := s.ApplySchema(context.Background(), schema); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		return dir
	}

	t.Run("torn WAL next to healthy main.lbug recovers", func(t *testing.T) {
		dir := setupDB(t)
		dbPath := filepath.Join(dir, "main.lbug")
		walPath := dbPath + ".wal"
		ckptPath := dbPath + ".wal.checkpoint"
		// Simulate the crash artifact: a torn WAL (and its checkpoint
		// companion) left next to a main.lbug the engine itself could still
		// parse.
		if err := os.WriteFile(walPath, []byte("torn wal"), 0600); err != nil {
			t.Fatalf("write torn WAL: %v", err)
		}
		if err := os.WriteFile(ckptPath, []byte("torn checkpoint"), 0600); err != nil {
			t.Fatalf("write torn checkpoint: %v", err)
		}
		recovered, err := Open(dir)
		if err != nil {
			t.Fatalf("Open with a torn WAL must recover, got %v", err)
		}
		defer closeStore(t, recovered)
		if _, err := os.Stat(walPath); !os.IsNotExist(err) {
			t.Fatalf("torn WAL companion survived corruption recovery: %v", err)
		}
		if _, err := os.Stat(ckptPath); !os.IsNotExist(err) {
			t.Fatalf("torn checkpoint artifact survived corruption recovery: %v", err)
		}
		if !recovered.TableExists("Component") {
			t.Fatal("recovery did not rehydrate the Component table from metadata")
		}
	})

	t.Run("corrupt main.lbug with torn WAL companions recovers", func(t *testing.T) {
		dir := setupDB(t)
		dbPath := filepath.Join(dir, "main.lbug")
		walPath := dbPath + ".wal"
		ckptPath := dbPath + ".wal.checkpoint"
		if err := os.WriteFile(dbPath, []byte("not a ladybug database"), 0600); err != nil {
			t.Fatalf("corrupt main.lbug: %v", err)
		}
		if err := os.WriteFile(walPath, []byte("torn wal"), 0600); err != nil {
			t.Fatalf("write torn WAL: %v", err)
		}
		if err := os.WriteFile(ckptPath, []byte("torn checkpoint"), 0600); err != nil {
			t.Fatalf("write torn checkpoint: %v", err)
		}
		recovered, err := Open(dir)
		if err != nil {
			t.Fatalf("Open after corrupt main + torn WAL must recover, got %v", err)
		}
		defer closeStore(t, recovered)
		if _, err := os.Stat(walPath); !os.IsNotExist(err) {
			t.Fatalf("torn WAL companion survived corruption recovery: %v", err)
		}
		if _, err := os.Stat(ckptPath); !os.IsNotExist(err) {
			t.Fatalf("torn checkpoint artifact survived corruption recovery: %v", err)
		}
		if !recovered.TableExists("Component") {
			t.Fatal("recovery did not rehydrate the Component table from metadata")
		}
	})
}

// TestOpenCleansOrphanedAtomicTempFiles pins the crash-window cleanup for the
// three atomic-write primitives (writeAtomicBranchState, stageSchemaMetadata,
// probePVCWritable): each stages its durable write in a temp file
// (.state-*.tmp / .schema-*.tmp / health-*.tmp) that is renamed onto its
// target (or, for the health probe, removed) only after the write is fully
// synced, so a crash at any point before that leaks the temp file.
// cleanupOrphanedTempBranches (service/recovery.go) deliberately skips them —
// branchKeyFromFile matches only .lbug/.schema.json/.state.json branch files —
// so without Open's sweep every crash leaks one small file per primitive
// forever. The sweep must remove every stranded temp family while leaving the
// live files — a branch's state/schema records (the renamed targets of the
// .state-* and .schema-* temps) and main's schema metadata — untouched.
func TestOpenCleansOrphanedAtomicTempFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
	if err := s.ApplySchema(context.Background(), schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Plant the crash residue: one temp from each atomic-write primitive, in
	// the directory each primitive writes to — branches/ for the branch state
	// and branch schema metadata, the data root for main's schema.json and the
	// health probe.
	orphans := []string{
		filepath.Join(dir, "branches", ".state-123456.tmp"),
		filepath.Join(dir, "branches", ".schema-234567.tmp"),
		filepath.Join(dir, ".schema-345678.tmp"),
		filepath.Join(dir, "health-456789.tmp"),
	}
	for _, p := range orphans {
		if err := os.WriteFile(p, []byte("torn temp"), 0600); err != nil {
			t.Fatalf("plant orphaned temp %q: %v", p, err)
		}
	}
	// Live files sharing the swept directories must survive the sweep: a
	// branch's durable state and schema records (named differently than any
	// temp pattern), plus main.lbug and schema.json produced by the setup
	// Open/ApplySchema above.
	live := []string{
		filepath.Join(dir, "branches", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.state.json"),
		filepath.Join(dir, "branches", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.schema.json"),
		filepath.Join(dir, "main.lbug"),
		filepath.Join(dir, "schema.json"),
	}
	for _, p := range live[:2] {
		if err := os.WriteFile(p, []byte("live record"), 0600); err != nil {
			t.Fatalf("plant live file %q: %v", p, err)
		}
	}
	for _, p := range live[2:] {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("live file %q missing before reopen: %v", p, err)
		}
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after crash residue: %v", err)
	}
	defer closeStore(t, reopened)
	for _, p := range orphans {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("orphaned temp %q survived Open cleanup: %v", p, err)
		}
	}
	for _, p := range live {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("live file %q was removed by Open cleanup: %v", p, err)
		}
	}
	if !reopened.TableExists("Component") {
		t.Fatal("schema metadata did not survive the sweep")
	}
}
