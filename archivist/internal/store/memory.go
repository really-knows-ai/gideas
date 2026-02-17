// Package store implements the in-memory Content-Addressable Storage (CAS)
// backend for the Archivist service.
//
// The architecture separates Content (raw bytes, deduplicated by SHA-256 hash)
// from Provenance (version history keyed by workitem + artefact). This split
// enables deduplication — identical content stored by different artefacts
// references the same blob entry.
package store

import (
	"sync"
	"time"
)

// ArtefactVersion records a single version in an artefact's history.
// The last entry in the slice is always the "Head" (current version).
type ArtefactVersion struct {
	Hash      string
	Kind      string
	CreatedAt time.Time
}

// MemoryStore is a thread-safe, in-memory CAS backend.
//
// It contains two maps:
//   - BlobStore: content_hash -> raw bytes (deduplication layer)
//   - ProvenanceStore: workitem_id -> artefact_id -> []ArtefactVersion (history)
type MemoryStore struct {
	mu              sync.RWMutex
	BlobStore       map[string][]byte                       // content_hash -> bytes
	ProvenanceStore map[string]map[string][]ArtefactVersion // workitem_id -> artefact_id -> versions
}

// NewMemoryStore returns an initialized MemoryStore ready for use.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		BlobStore:       make(map[string][]byte),
		ProvenanceStore: make(map[string]map[string][]ArtefactVersion),
	}
}

// StoreBlob writes raw bytes to the BlobStore if not already present.
// Returns true if the blob was newly written, false if it already existed.
func (m *MemoryStore) StoreBlob(hash string, content []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.BlobStore[hash]; exists {
		return false
	}
	// Copy content to avoid aliasing caller's slice.
	buf := make([]byte, len(content))
	copy(buf, content)
	m.BlobStore[hash] = buf
	return true
}

// GetBlob retrieves raw bytes by content hash.
// Returns nil, false if the hash is not found.
func (m *MemoryStore) GetBlob(hash string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, ok := m.BlobStore[hash]
	return data, ok
}

// AppendVersion adds a new version entry to the provenance history for
// the given (workitemID, artefactID) pair. It does NOT check for
// duplicates — the caller must perform dedup logic before calling this.
func (m *MemoryStore) AppendVersion(workitemID, artefactID, hash, kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ProvenanceStore[workitemID] == nil {
		m.ProvenanceStore[workitemID] = make(map[string][]ArtefactVersion)
	}
	m.ProvenanceStore[workitemID][artefactID] = append(
		m.ProvenanceStore[workitemID][artefactID],
		ArtefactVersion{
			Hash:      hash,
			Kind:      kind,
			CreatedAt: time.Now(),
		},
	)
}

// GetHistory returns the full version history for (workitemID, artefactID).
// Returns nil if no versions exist.
func (m *MemoryStore) GetHistory(workitemID, artefactID string) []ArtefactVersion {
	m.mu.RLock()
	defer m.mu.RUnlock()

	artefacts, ok := m.ProvenanceStore[workitemID]
	if !ok {
		return nil
	}
	return artefacts[artefactID]
}

// GetHead returns the most recent version for (workitemID, artefactID).
// Returns nil if no versions exist.
func (m *MemoryStore) GetHead(workitemID, artefactID string) *ArtefactVersion {
	history := m.GetHistory(workitemID, artefactID)
	if len(history) == 0 {
		return nil
	}
	return &history[len(history)-1]
}

// ArtefactEntry is a summary of a single artefact for listing purposes.
type ArtefactEntry struct {
	ID   string
	Kind string
}

// ListArtefacts returns all artefact IDs and their kinds for a workitem.
func (m *MemoryStore) ListArtefacts(workitemID string) []ArtefactEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	artefacts, ok := m.ProvenanceStore[workitemID]
	if !ok {
		return nil
	}
	entries := make([]ArtefactEntry, 0, len(artefacts))
	for id, versions := range artefacts {
		if len(versions) > 0 {
			entries = append(entries, ArtefactEntry{
				ID:   id,
				Kind: versions[len(versions)-1].Kind,
			})
		}
	}
	return entries
}
