package subscriber

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// FileCheckpoint persists per-channel last-sequence values to a JSON file.
// This is minimal state — a single file with channel→sequence mappings —
// not a data store (per Service Invariant #16: the Flow Monitor is stateless
// aside from the replay checkpoint).
type FileCheckpoint struct {
	path string
	mu   sync.Mutex
	data map[string]uint64
}

// NewFileCheckpoint loads (or creates) a checkpoint file at the given path.
func NewFileCheckpoint(path string) (*FileCheckpoint, error) {
	cp := &FileCheckpoint{
		path: path,
		data: make(map[string]uint64),
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cp, nil
		}
		return nil, fmt.Errorf("read checkpoint %s: %w", path, err)
	}

	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cp.data); err != nil {
			return nil, fmt.Errorf("parse checkpoint %s: %w", path, err)
		}
	}

	return cp, nil
}

// Get returns the last-seen sequence for the given channel, or 0 if none.
func (cp *FileCheckpoint) Get(channel string) (uint64, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.data[channel], nil
}

// Set persists the last-seen sequence for the given channel.
func (cp *FileCheckpoint) Set(channel string, seq uint64) error {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	cp.data[channel] = seq

	raw, err := json.Marshal(cp.data)
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	if err := os.WriteFile(cp.path, raw, 0o644); err != nil {
		return fmt.Errorf("write checkpoint %s: %w", cp.path, err)
	}

	return nil
}
