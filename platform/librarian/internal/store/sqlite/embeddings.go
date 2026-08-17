// Embedding and vector-similarity operations for the Librarian SQLite store.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// GetEmbedding reads the embedding BLOB for a specific version.
func (s *Store) GetEmbedding(ctx context.Context, lawID, versionHash string) ([]float32, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT embedding FROM law_versions WHERE law_id = ? AND version_hash = ?`,
		lawID, versionHash,
	).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("version %s/%s not found", lawID, versionHash)
	}
	if err != nil {
		return nil, fmt.Errorf("get embedding: %w", err)
	}
	return decodeEmbedding(blob), nil
}

// SetEmbedding writes an embedding BLOB for a specific version.
func (s *Store) SetEmbedding(ctx context.Context, lawID, versionHash string, embedding []float32) error {
	blob := encodeEmbedding(embedding)
	res, err := s.db.ExecContext(ctx,
		`UPDATE law_versions SET embedding = ? WHERE law_id = ? AND version_hash = ?`,
		blob, lawID, versionHash,
	)
	if err != nil {
		return fmt.Errorf("set embedding: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("version %s/%s not found", lawID, versionHash)
	}
	return nil
}

// GetAllActiveEmbeddings returns all (lawID, versionHash, appliesTo, embedding)
// pairs for active laws that have embeddings. Used by conflict detection.
func (s *Store) GetAllActiveEmbeddings(ctx context.Context) ([]LawEmbedding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT lv.law_id, lv.version_hash, lv.applies_to_json, lv.embedding
		FROM law_versions lv
		INNER JOIN laws l ON lv.law_id = l.id
		WHERE l.active = 1 AND lv.embedding IS NOT NULL
		AND lv.version_hash = (
			SELECT lv2.version_hash FROM law_versions lv2
			WHERE lv2.law_id = lv.law_id
			ORDER BY lv2.rowid DESC LIMIT 1
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("query active embeddings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []LawEmbedding
	for rows.Next() {
		var (
			le     LawEmbedding
			atJSON string
			blob   []byte
		)
		if err := rows.Scan(&le.LawID, &le.VersionHash, &atJSON, &blob); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		at, err := unmarshalAppliesTo(atJSON)
		if err != nil {
			return nil, err
		}
		le.AppliesTo = at
		le.Embedding = decodeEmbedding(blob)
		results = append(results, le)
	}
	return results, rows.Err()
}

// UpsertVecEmbedding inserts or replaces the embedding for a law in the
// law_embeddings vec0 virtual table. The mapping between law_id (text) and
// the integer rowid required by vec0 is maintained in law_embedding_map.
func (s *Store) UpsertVecEmbedding(ctx context.Context, lawID string, embedding []float32) error {
	if len(embedding) != s.embeddingDims {
		return fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(embedding), s.embeddingDims)
	}

	blob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("serialize embedding: %w", err)
	}

	// Check if a mapping already exists.
	var existingRowID int64
	err = s.db.QueryRowContext(ctx,
		`SELECT rowid_ref FROM law_embedding_map WHERE law_id = ?`, lawID,
	).Scan(&existingRowID)

	if err == nil {
		// Update existing: delete old vec row and insert new one with the same rowid.
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM law_embeddings WHERE rowid = ?`, existingRowID,
		); err != nil {
			return fmt.Errorf("delete old vec embedding: %w", err)
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO law_embeddings(rowid, embedding) VALUES (?, ?)`,
			existingRowID, blob,
		); err != nil {
			return fmt.Errorf("update vec embedding: %w", err)
		}
		return nil
	}

	if err != sql.ErrNoRows {
		return fmt.Errorf("check existing embedding map: %w", err)
	}

	// Insert new: let vec0 assign a rowid, then record the mapping.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO law_embeddings(embedding) VALUES (?)`, blob,
	)
	if err != nil {
		return fmt.Errorf("insert vec embedding: %w", err)
	}
	newRowID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get last insert id: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO law_embedding_map(law_id, rowid_ref) VALUES (?, ?)`,
		lawID, newRowID,
	); err != nil {
		return fmt.Errorf("insert embedding map: %w", err)
	}

	return nil
}

// DeleteVecEmbedding removes the embedding for a law from the vec0 virtual
// table and the mapping table. No error is returned if no embedding exists
// for the law.
func (s *Store) DeleteVecEmbedding(ctx context.Context, lawID string) error {
	var rowIDRef int64
	err := s.db.QueryRowContext(ctx,
		`SELECT rowid_ref FROM law_embedding_map WHERE law_id = ?`, lawID,
	).Scan(&rowIDRef)
	if err == sql.ErrNoRows {
		return nil // No embedding to delete — not an error.
	}
	if err != nil {
		return fmt.Errorf("lookup embedding map: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM law_embeddings WHERE rowid = ?`, rowIDRef,
	); err != nil {
		return fmt.Errorf("delete vec embedding: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM law_embedding_map WHERE law_id = ?`, lawID,
	); err != nil {
		return fmt.Errorf("delete embedding map: %w", err)
	}

	return nil
}

// VecSearchResult represents a single result from a vector similarity search.
type VecSearchResult struct {
	LawID    string
	Distance float64 // L2 distance from sqlite-vec (lower = more similar)
}

// SearchVecSimilar performs a k-nearest-neighbour search against the
// law_embeddings vec0 virtual table using the provided query embedding.
// Results are returned ordered by ascending distance (most similar first).
// The limit parameter controls the maximum number of results.
func (s *Store) SearchVecSimilar(ctx context.Context, queryEmbedding []float32, limit int) ([]VecSearchResult, error) {
	if len(queryEmbedding) != s.embeddingDims {
		return nil, fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(queryEmbedding), s.embeddingDims)
	}
	if limit <= 0 {
		limit = 10
	}

	blob, err := sqlite_vec.SerializeFloat32(queryEmbedding)
	if err != nil {
		return nil, fmt.Errorf("serialize query embedding: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT le.rowid, le.distance
		 FROM law_embeddings le
		 WHERE le.embedding MATCH ?
		 ORDER BY le.distance
		 LIMIT ?`,
		blob, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("vec search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Collect rowid+distance pairs first, then close the cursor before
	// issuing follow-up queries (SQLite in-memory requires this).
	type rowResult struct {
		rowID    int64
		distance float64
	}
	var rawResults []rowResult
	for rows.Next() {
		var r rowResult
		if err := rows.Scan(&r.rowID, &r.distance); err != nil {
			return nil, fmt.Errorf("scan vec result: %w", err)
		}
		rawResults = append(rawResults, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	// Resolve rowid -> law_id via the mapping table.
	var results []VecSearchResult
	for _, r := range rawResults {
		var lawID string
		err := s.db.QueryRowContext(ctx,
			`SELECT law_id FROM law_embedding_map WHERE rowid_ref = ?`, r.rowID,
		).Scan(&lawID)
		if err != nil {
			continue // Orphaned row — skip.
		}
		results = append(results, VecSearchResult{
			LawID:    lawID,
			Distance: r.distance,
		})
	}

	return results, nil
}
