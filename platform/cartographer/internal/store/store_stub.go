//go:build !ladybug

package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/foundry/flow/cartographer/internal/schema"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

// Column names reserved for entity types (cannot be used as property names).
const reservedColumnProperties = "_properties"

// ladybugDB is the concrete in-memory implementation of Store.
// This stub is used when the LadybugDB C library is not available (default build).
// ponytail: All data is stored in Go maps, not in an actual LadybugDB instance.
// The stub is functionally complete for testing and development but provides
// no persistence or crash recovery. Replace with the LadybugDB-backed
// implementation (build tag: ladybug) for production.
type ladybugDB struct {
	mu         sync.Mutex
	dataDir    string
	isInMemory bool
	closed     bool

	// Schema cache (from the last applied schema)
	entityTypeDefs map[string]*EntityTypeDef
	edgeTypeDefs   map[string]*EdgeTypeDef
	ruleIndex      map[string][]*flowv1.ConnectionRule // entityType -> rules

	// Data stores
	entities map[string]*Entity // id -> Entity
	edges    map[string]*Edge   // id -> Edge

	// Vector index state (per entity type)
	bootstrapped map[string]bool
	vecDimension map[string]int

	// Branch databases (txID -> branchDB)
	branches map[string]*branchDB
}

// branchDB is an in-memory branch database for transaction isolation.
type branchDB struct {
	mu             sync.Mutex
	entities       map[string]*Entity
	edges          map[string]*Edge
	entityTypeDefs map[string]*EntityTypeDef
	edgeTypeDefs   map[string]*EdgeTypeDef
	ruleIndex      map[string][]*flowv1.ConnectionRule
	bootstrapped   map[string]bool
	vecDimension   map[string]int
}

// Open opens or creates a LadybugDB database at the given filesystem path.
// ponytail: The stub ignores the path and creates an in-memory store.
// The real LadybugDB implementation opens a database file.
func Open(path string) (Store, error) {
	db := &ladybugDB{
		dataDir:        path,
		entityTypeDefs: make(map[string]*EntityTypeDef),
		edgeTypeDefs:   make(map[string]*EdgeTypeDef),
		ruleIndex:      make(map[string][]*flowv1.ConnectionRule),
		entities:       make(map[string]*Entity),
		edges:          make(map[string]*Edge),
		bootstrapped:   make(map[string]bool),
		vecDimension:   make(map[string]int),
		branches:       make(map[string]*branchDB),
	}
	return db, nil
}

// OpenInMemory opens an ephemeral in-memory LadybugDB database.
// Used for tests only.
func OpenInMemory() (Store, error) {
	db := &ladybugDB{
		isInMemory:     true,
		entityTypeDefs: make(map[string]*EntityTypeDef),
		edgeTypeDefs:   make(map[string]*EdgeTypeDef),
		ruleIndex:      make(map[string][]*flowv1.ConnectionRule),
		entities:       make(map[string]*Entity),
		edges:          make(map[string]*Edge),
		bootstrapped:   make(map[string]bool),
		vecDimension:   make(map[string]int),
		branches:       make(map[string]*branchDB),
	}
	return db, nil
}

// Close closes the store and releases resources.
func (db *ladybugDB) Close() error {
	if db.closed {
		return nil
	}
	db.closed = true
	return nil
}

// --- Schema methods ---

func (db *ladybugDB) ApplySchema(ctx context.Context, s *flowv1.Schema) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return ErrDatabaseNotReady
	}

	// Validate first
	if err := ValidateSchemaInternal(s); err != nil {
		return err
	}

	// Apply entity types
	for _, et := range s.EntityTypes {
		if existing, ok := db.entityTypeDefs[et.Name]; ok {
			// Check for incompatible (destructive) changes
			if err := checkEntityTypeCompatibility(existing, et); err != nil {
				return err
			}
			// Merge new properties (additive)
			existingProps := make(map[string]bool)
			for _, p := range existing.Properties {
				existingProps[p.Name] = true
			}
			for _, p := range et.Properties {
				if !existingProps[p.Name] {
					existing.Properties = append(existing.Properties, PropertyDef{
						Name:     p.Name,
						Type:     p.Type,
						Required: p.Required,
					})
				}
			}
			// Update vector index flag (false -> true is OK, true -> false is destructive)
			if !existing.EnableVectorIndex && et.EnableVectorIndex {
				existing.EnableVectorIndex = true
			}
		} else {
			def := &EntityTypeDef{
				Name:              et.Name,
				EnableVectorIndex: et.EnableVectorIndex,
			}
			for _, p := range et.Properties {
				def.Properties = append(def.Properties, PropertyDef{
					Name:     p.Name,
					Type:     p.Type,
					Required: p.Required,
				})
			}
			db.entityTypeDefs[et.Name] = def
		}

		// Update rule index (empty/nil rules list clears rules, meaning no edges permitted).
		clone := make([]*flowv1.ConnectionRule, len(et.Rules))
		for i, r := range et.Rules {
			clone[i] = &flowv1.ConnectionRule{
				CanConnectTo: append([]string{}, r.CanConnectTo...),
				Using:        append([]string{}, r.Using...),
			}
		}
		db.ruleIndex[et.Name] = clone
	}

	// Apply edge types
	for _, et := range s.EdgeTypes {
		if existing, ok := db.edgeTypeDefs[et.Name]; ok {
			// Check for incompatible (destructive) changes
			if err := checkEdgeTypeCompatibility(existing, et); err != nil {
				return err
			}
			// Merge new properties (additive)
			existingProps := make(map[string]bool)
			for _, p := range existing.Properties {
				existingProps[p.Name] = true
			}
			for _, p := range et.Properties {
				if !existingProps[p.Name] {
					existing.Properties = append(existing.Properties, PropertyDef{
						Name:     p.Name,
						Type:     p.Type,
						Required: p.Required,
					})
				}
			}
		} else {
			def := &EdgeTypeDef{
				Name: et.Name,
			}
			for _, p := range et.Properties {
				def.Properties = append(def.Properties, PropertyDef{
					Name:     p.Name,
					Type:     p.Type,
					Required: p.Required,
				})
			}
			db.edgeTypeDefs[et.Name] = def
		}
	}

	return nil
}

// checkEdgeTypeCompatibility checks that the new schema does not make destructive changes
// to an existing edge type. Returns ErrTableStructureMismatch if incompatible.
func checkEdgeTypeCompatibility(existing *EdgeTypeDef, newET *flowv1.EdgeType) error {
	// Check for removed properties
	existingProps := make(map[string]bool)
	for _, p := range existing.Properties {
		existingProps[p.Name] = true
	}
	for _, p := range newET.Properties {
		delete(existingProps, p.Name)
	}
	// Any remaining name in existingProps means it was removed
	for name := range existingProps {
		if name == "id" || name == "from" || name == "to" || name == "type" {
			continue
		}
		return fmt.Errorf("%w: property %q was removed from edge type %q", ErrTableStructureMismatch, name, newET.Name)
	}
	return nil
}

// checkEntityTypeCompatibility checks that the new schema does not make destructive changes
// to an existing entity type. Returns ErrTableStructureMismatch if incompatible.
func checkEntityTypeCompatibility(existing *EntityTypeDef, newET *flowv1.EntityType) error {
	// Check for removed properties
	existingProps := make(map[string]bool)
	for _, p := range existing.Properties {
		existingProps[p.Name] = true
	}
	for _, p := range newET.Properties {
		delete(existingProps, p.Name)
	}
	// Any remaining name in existingProps means it was removed
	for name := range existingProps {
		// Skip reserved columns that might be in our schema cache
		if name == "id" || name == reservedColumnProperties || name == "embedding" {
			continue
		}
		return fmt.Errorf("%w: property %q was removed from entity type %q", ErrTableStructureMismatch, name, newET.Name)
	}

	// Check vector index flag change (true -> false is destructive)
	if existing.EnableVectorIndex && !newET.EnableVectorIndex {
		return fmt.Errorf("%w: cannot disable vector index on entity type %q", ErrTableStructureMismatch, newET.Name)
	}

	return nil
}

// ValidateSchemaInternal is the schema-level validation function used by the store stub.
func ValidateSchemaInternal(s *flowv1.Schema) error {
	return schema.Validate(s)
}

func (db *ladybugDB) TableExists(entityType string) bool {
	_, ok := db.entityTypeDefs[entityType]
	return ok
}

func (db *ladybugDB) ListMainEntityTypes() ([]string, error) {
	names := make([]string, 0, len(db.entityTypeDefs))
	for name := range db.entityTypeDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (db *ladybugDB) ValidateSchema(ctx context.Context, s *flowv1.Schema) error {
	return ValidateSchemaInternal(s)
}

func (db *ladybugDB) EntityTypeNames() []string {
	names := make([]string, 0, len(db.entityTypeDefs))
	for name := range db.entityTypeDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (db *ladybugDB) EdgeTypeNames() []string {
	names := make([]string, 0, len(db.edgeTypeDefs))
	for name := range db.edgeTypeDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (db *ladybugDB) EntityType(name string) (*EntityTypeDef, bool) {
	def, ok := db.entityTypeDefs[name]
	if !ok {
		return nil, false
	}
	return def, true
}

func (db *ladybugDB) EdgeType(name string) (*EdgeTypeDef, bool) {
	def, ok := db.edgeTypeDefs[name]
	if !ok {
		return nil, false
	}
	return def, true
}

// --- Entity CRUD ---

func (db *ladybugDB) CreateEntity(ctx context.Context, entityType, id string, properties map[string]string, embedding []float32, branch string) (*Entity, error) {
	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		// Route to branch DB
		return db.createEntityInBranch(br, entityType, id, properties, embedding)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	return db.createEntityLocked(entityType, id, properties, embedding)
}

// createEntityInBranch creates an entity in a branch DB.
func (db *ladybugDB) createEntityInBranch(br *branchDB, entityType, id string, properties map[string]string, embedding []float32) (*Entity, error) {
	def, ok := br.entityTypeDefs[entityType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEntityType, entityType)
	}
	if id == "" {
		id = uuid.New().String()
	} else {
		if err := validateUUID(id); err != nil {
			return nil, err
		}
	}
	if _, exists := br.entities[id]; exists {
		return nil, fmt.Errorf("%w: entity with id %q already exists", ErrEntityAlreadyExists, id)
	}
	propDefs := make(map[string]PropertyDef)
	for _, p := range def.Properties {
		propDefs[p.Name] = p
	}
	for key := range properties {
		if _, ok := propDefs[key]; !ok {
			return nil, fmt.Errorf("%w: %q for entity type %q", ErrUnknownProperty, key, entityType)
		}
	}
	for _, p := range def.Properties {
		if p.Required {
			if _, ok := properties[p.Name]; !ok {
				return nil, fmt.Errorf("%w: %q for entity type %q", ErrMissingRequiredProperty, p.Name, entityType)
			}
		}
	}
	// Validate embedding using branch state
	if err := br.validateBranchEmbedding(def, embedding, entityType); err != nil {
		return nil, err
	}

	// Store embedding if type is indexed and embedding is non-nil
	var storedEmbedding []float32
	if def.EnableVectorIndex && embedding != nil && len(embedding) > 0 {
		storedEmbedding = embedding
	} else if !def.EnableVectorIndex {
		// Non-indexed type: discard embedding (but validate NaN/inf)
		storedEmbedding = nil
	} else {
		storedEmbedding = nil
	}

	props := make(map[string]string, len(properties))
	maps.Copy(props, properties)
	now := time.Now().UTC()
	entity := &Entity{Id: id, Type: entityType, Properties: props, Embedding: storedEmbedding, CreatedAt: now, UpdatedAt: now}
	br.entities[id] = entity
	return entity, nil
}

// createEntityLocked creates an entity assuming the write lock is already held.
func (db *ladybugDB) createEntityLocked(entityType, id string, properties map[string]string, embedding []float32) (*Entity, error) {
	if db.closed {
		return nil, ErrDatabaseNotReady
	}

	def, ok := db.entityTypeDefs[entityType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEntityType, entityType)
	}

	// Validate ID or auto-generate
	if id == "" {
		id = uuid.New().String()
	} else {
		if err := validateUUID(id); err != nil {
			return nil, err
		}
	}

	// Check for duplicate
	if _, exists := db.entities[id]; exists {
		return nil, fmt.Errorf("%w: entity with id %q already exists", ErrEntityAlreadyExists, id)
	}

	// Validate properties against schema
	propDefs := make(map[string]PropertyDef)
	for _, p := range def.Properties {
		propDefs[p.Name] = p
	}

	// Check unknown properties
	for key := range properties {
		if _, ok := propDefs[key]; !ok {
			return nil, fmt.Errorf("%w: %q for entity type %q", ErrUnknownProperty, key, entityType)
		}
	}

	// Check required properties
	for _, p := range def.Properties {
		if p.Required {
			if _, ok := properties[p.Name]; !ok {
				return nil, fmt.Errorf("%w: %q for entity type %q", ErrMissingRequiredProperty, p.Name, entityType)
			}
		}
	}

	// Values are map[string]string so they're always strings; empty is allowed
	_ = properties

	// Validate embedding
	if err := db.validateEmbeddingForEntity(def, embedding, entityType); err != nil {
		return nil, err
	}

	// Store embedding if type is indexed and embedding is non-nil
	var storedEmbedding []float32
	if def.EnableVectorIndex && embedding != nil && len(embedding) > 0 {
		storedEmbedding = embedding
	} else if !def.EnableVectorIndex {
		// Non-indexed type: discard embedding (but validate NaN/inf)
		storedEmbedding = nil
	} else {
		storedEmbedding = nil
	}

	// Copy properties
	props := make(map[string]string, len(properties))
	maps.Copy(props, properties)

	now := time.Now().UTC()
	entity := &Entity{
		Id:         id,
		Type:       entityType,
		Properties: props,
		Embedding:  storedEmbedding,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	db.entities[id] = entity
	return entity, nil
}

// validateEmbeddingForEntity validates the embedding against the entity type's schema.
func (db *ladybugDB) validateEmbeddingForEntity(def *EntityTypeDef, embedding []float32, entityType string) error {
	// NaN/infinity check applies regardless of indexed status
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return ErrNaNOrInfEmbedding
		}
	}

	if !def.EnableVectorIndex {
		return nil // Non-indexed type: discard embedding after NaN check
	}

	if len(embedding) == 0 {
		// Bootstrap check: first entity MUST include embedding
		if !db.bootstrapped[entityType] {
			return fmt.Errorf("%w: entity type %q has vector index enabled but no embedding provided", ErrVectorBootstrap, entityType)
		}
		return nil // Subsequent entities may omit embedding
	}

	// Validate dimension
	dim, bootstrapped := db.vecDimension[entityType]
	if !bootstrapped {
		// First embedding write: bootstrap the dimension
		db.bootstrapped[entityType] = true
		db.vecDimension[entityType] = len(embedding)
		return nil
	}

	if len(embedding) != dim {
		return fmt.Errorf("%w: expected dimension %d, got %d", ErrEmbeddingDimension, dim, len(embedding))
	}

	return nil
}

// validateBranchEmbedding validates the embedding against the entity type's schema using branch state.
func (br *branchDB) validateBranchEmbedding(def *EntityTypeDef, embedding []float32, entityType string) error {
	// NaN/infinity check applies regardless of indexed status
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return ErrNaNOrInfEmbedding
		}
	}

	if !def.EnableVectorIndex {
		return nil // Non-indexed type: discard embedding after NaN check
	}

	if len(embedding) == 0 {
		// Bootstrap check: first entity MUST include embedding
		if !br.bootstrapped[entityType] {
			return fmt.Errorf("%w: entity type %q has vector index enabled but no embedding provided", ErrVectorBootstrap, entityType)
		}
		return nil // Subsequent entities may omit embedding
	}

	// Validate dimension
	dim, bootstrapped := br.vecDimension[entityType]
	if !bootstrapped {
		// First embedding write: bootstrap the dimension
		br.bootstrapped[entityType] = true
		br.vecDimension[entityType] = len(embedding)
		return nil
	}

	if len(embedding) != dim {
		return fmt.Errorf("%w: expected dimension %d, got %d", ErrEmbeddingDimension, dim, len(embedding))
	}

	return nil
}

func (db *ladybugDB) UpdateEntity(ctx context.Context, id string, properties map[string]string, embedding []float32, branch string) (*Entity, error) {
	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		return db.updateEntityInBranch(br, id, properties, embedding)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	return db.updateEntityLocked(id, properties, embedding)
}

func (db *ladybugDB) updateEntityInBranch(br *branchDB, id string, properties map[string]string, embedding []float32) (*Entity, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}
	existing, ok := br.entities[id]
	if !ok {
		return nil, fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, id)
	}
	// Look up entity type for embedding validation
	def, ok := br.entityTypeDefs[existing.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEntityType, existing.Type)
	}

	// Validate properties
	propDefs := make(map[string]bool)
	for _, p := range def.Properties {
		propDefs[p.Name] = true
	}
	for key := range properties {
		if !propDefs[key] {
			return nil, fmt.Errorf("%w: %q for entity type %q", ErrUnknownProperty, key, existing.Type)
		}
	}

	// Embedding validation using branch state
	hasNewEmbedding := len(embedding) > 0
	if err := br.validateBranchUpdateEmbedding(def, embedding, existing.Type, hasNewEmbedding); err != nil {
		return nil, err
	}

	// Apply updates
	maps.Copy(existing.Properties, properties)

	if def.EnableVectorIndex && embedding != nil && hasNewEmbedding {
		existing.Embedding = embedding
	}
	// ponytail: Non-indexed types discard embedding after NaN check above.
	// Empty/nil embedding on indexed types preserves the existing value.

	existing.UpdatedAt = time.Now().UTC()
	return existing, nil
}

func (db *ladybugDB) updateEntityLocked(id string, properties map[string]string, embedding []float32) (*Entity, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	existing, ok := db.entities[id]
	if !ok {
		return nil, fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, id)
	}

	def, ok := db.entityTypeDefs[existing.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEntityType, existing.Type)
	}

	// Validate properties
	propDefs := make(map[string]bool)
	for _, p := range def.Properties {
		propDefs[p.Name] = true
	}
	for key := range properties {
		if !propDefs[key] {
			return nil, fmt.Errorf("%w: %q for entity type %q", ErrUnknownProperty, key, existing.Type)
		}
	}

	// Embedding validation
	hasNewEmbedding := len(embedding) > 0
	if err := db.validateUpdateEmbedding(def, embedding, existing.Type, existing.Embedding, hasNewEmbedding); err != nil {
		return nil, err
	}

	// Apply updates
	maps.Copy(existing.Properties, properties)

	if def.EnableVectorIndex && embedding != nil && hasNewEmbedding {
		existing.Embedding = embedding
	}
	// ponytail: Non-indexed types discard embedding after NaN check above.
	// Empty/nil embedding on indexed types preserves the existing value.

	existing.UpdatedAt = time.Now().UTC()
	return existing, nil
}

func (db *ladybugDB) validateUpdateEmbedding(def *EntityTypeDef, embedding []float32, entityType string, existingEmbedding []float32, hasNewEmbedding bool) error {
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return ErrNaNOrInfEmbedding
		}
	}

	if !def.EnableVectorIndex {
		return nil // Non-indexed: discard after NaN check
	}

	if !hasNewEmbedding {
		return nil // Preserve existing
	}

	// Check dimension match against established dimension
	if dim, bootstrapped := db.vecDimension[entityType]; bootstrapped {
		if len(embedding) != dim {
			return fmt.Errorf("%w: expected dimension %d, got %d", ErrEmbeddingDimension, dim, len(embedding))
		}
	} else if len(embedding) > 0 {
		// Bootstrap with this update
		db.bootstrapped[entityType] = true
		db.vecDimension[entityType] = len(embedding)
	}

	return nil
}

// validateBranchUpdateEmbedding validates embedding update using branch state.
func (br *branchDB) validateBranchUpdateEmbedding(def *EntityTypeDef, embedding []float32, entityType string, hasNewEmbedding bool) error {
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return ErrNaNOrInfEmbedding
		}
	}

	if !def.EnableVectorIndex {
		return nil // Non-indexed: discard after NaN check
	}

	if !hasNewEmbedding {
		return nil // Preserve existing
	}

	// Check dimension match against established dimension
	if dim, bootstrapped := br.vecDimension[entityType]; bootstrapped {
		if len(embedding) != dim {
			return fmt.Errorf("%w: expected dimension %d, got %d", ErrEmbeddingDimension, dim, len(embedding))
		}
	} else if len(embedding) > 0 {
		// Bootstrap with this update
		br.bootstrapped[entityType] = true
		br.vecDimension[entityType] = len(embedding)
	}

	return nil
}

func (db *ladybugDB) DeleteEntity(ctx context.Context, id, branch string) (*Entity, error) {
	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		ent, ok := br.entities[id]
		if !ok {
			return nil, fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, id)
		}
		// Cascade-delete edges referencing this entity
		for eid, edge := range br.edges {
			if edge.FromEntityID == id || edge.ToEntityID == id {
				delete(br.edges, eid)
			}
		}
		delete(br.entities, id)
		return ent, nil
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	return db.deleteEntityLocked(id)
}

func (db *ladybugDB) deleteEntityLocked(id string) (*Entity, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	entity, ok := db.entities[id]
	if !ok {
		return nil, fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, id)
	}

	// WAL: record edges to delete (simulated in-memory for stub)
	edgeIDs := make([]string, 0)
	for eid, edge := range db.edges {
		if edge.FromEntityID == id || edge.ToEntityID == id {
			edgeIDs = append(edgeIDs, eid)
		}
	}

	// Delete cascade: remove all edges referencing this entity
	for _, eid := range edgeIDs {
		delete(db.edges, eid)
	}

	// Delete the entity
	delete(db.entities, id)

	return entity, nil
}

func (db *ladybugDB) GetEntity(ctx context.Context, id, branch string) (*Entity, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		entity, ok := br.entities[id]
		if !ok {
			return nil, fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, id)
		}
		ent := *entity
		return &ent, nil
	}

	entity, ok := db.entities[id]
	if !ok {
		return nil, fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, id)
	}

	// Return a copy
	ent := *entity
	if ent.Properties != nil {
		props := make(map[string]string, len(ent.Properties))
		maps.Copy(props, ent.Properties)
		ent.Properties = props
	}
	if ent.Embedding != nil {
		emb := make([]float32, len(ent.Embedding))
		copy(emb, ent.Embedding)
		ent.Embedding = emb
	}
	return &ent, nil
}

// --- Edge CRUD ---

func (db *ladybugDB) CreateEdge(ctx context.Context, edgeType, fromID, toID string, properties map[string]string, branch string) (*Edge, error) {
	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		return db.createEdgeInBranch(br, edgeType, fromID, toID, properties)
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	return db.createEdgeLocked(edgeType, fromID, toID, properties)
}

func (db *ladybugDB) createEdgeInBranch(br *branchDB, edgeType, fromID, toID string, properties map[string]string) (*Edge, error) {
	def, ok := br.edgeTypeDefs[edgeType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEdgeType, edgeType)
	}
	if err := validateUUID(fromID); err != nil {
		return nil, err
	}
	if err := validateUUID(toID); err != nil {
		return nil, err
	}
	sourceEntity, ok := br.entities[fromID]
	if !ok {
		return nil, fmt.Errorf("%w: source entity %q not found", ErrSourceOrTargetNotFound, fromID)
	}
	if _, ok := br.entities[toID]; !ok {
		return nil, fmt.Errorf("%w: target entity %q not found", ErrSourceOrTargetNotFound, toID)
	}
	propDefs := make(map[string]PropertyDef)
	for _, p := range def.Properties {
		propDefs[p.Name] = p
	}
	for key := range properties {
		if _, ok := propDefs[key]; !ok {
			return nil, fmt.Errorf("%w: %q for edge type %q", ErrUnknownProperty, key, edgeType)
		}
	}
	for _, p := range def.Properties {
		if p.Required {
			if _, ok := properties[p.Name]; !ok {
				return nil, fmt.Errorf("%w: %q for edge type %q", ErrMissingRequiredProperty, p.Name, edgeType)
			}
		}
	}
	// Validate edge rules
	if err := br.validateEdgeRulesLocked(sourceEntity.Type, br.entities[toID].Type, edgeType); err != nil {
		return nil, err
	}
	edgeID := uuid.New().String()
	props := make(map[string]string, len(properties))
	maps.Copy(props, properties)
	now := time.Now().UTC()
	edge := &Edge{Id: edgeID, Type: edgeType, FromEntityID: fromID, ToEntityID: toID, Properties: props, CreatedAt: now, UpdatedAt: now}
	br.edges[edgeID] = edge
	return edge, nil
}

func (db *ladybugDB) createEdgeLocked(edgeType, fromID, toID string, properties map[string]string) (*Edge, error) {
	// Validate edge type exists
	def, ok := db.edgeTypeDefs[edgeType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEdgeType, edgeType)
	}

	// Validate UUIDs
	if err := validateUUID(fromID); err != nil {
		return nil, err
	}
	if err := validateUUID(toID); err != nil {
		return nil, err
	}

	// Source and target must exist
	sourceEntity, ok := db.entities[fromID]
	if !ok {
		return nil, fmt.Errorf("%w: source entity %q not found", ErrSourceOrTargetNotFound, fromID)
	}
	if _, ok := db.entities[toID]; !ok {
		return nil, fmt.Errorf("%w: target entity %q not found", ErrSourceOrTargetNotFound, toID)
	}

	// Validate properties against schema
	propDefs := make(map[string]PropertyDef)
	for _, p := range def.Properties {
		propDefs[p.Name] = p
	}

	for key := range properties {
		if _, ok := propDefs[key]; !ok {
			return nil, fmt.Errorf("%w: %q for edge type %q", ErrUnknownProperty, key, edgeType)
		}
	}

	for _, p := range def.Properties {
		if p.Required {
			if _, ok := properties[p.Name]; !ok {
				return nil, fmt.Errorf("%w: %q for edge type %q", ErrMissingRequiredProperty, p.Name, edgeType)
			}
		}
	}

	// Validate edge rules (source entity type's rules)
	if err := db.validateEdgeRulesLocked(sourceEntity.Type, db.entities[toID].Type, edgeType); err != nil {
		return nil, err
	}

	// Generate edge ID
	edgeID := uuid.New().String()

	props := make(map[string]string, len(properties))
	maps.Copy(props, properties)

	now := time.Now().UTC()
	edge := &Edge{
		Id:           edgeID,
		Type:         edgeType,
		FromEntityID: fromID,
		ToEntityID:   toID,
		Properties:   props,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	db.edges[edgeID] = edge
	return edge, nil
}

func (db *ladybugDB) DeleteEdge(ctx context.Context, id, branch string) (*Edge, error) {
	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		edge, ok := br.edges[id]
		if !ok {
			return nil, fmt.Errorf("%w: edge with id %q", ErrEdgeNotFound, id)
		}
		delete(br.edges, id)
		return edge, nil
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	return db.deleteEdgeLocked(id)
}

func (db *ladybugDB) deleteEdgeLocked(id string) (*Edge, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	edge, ok := db.edges[id]
	if !ok {
		return nil, fmt.Errorf("%w: edge with id %q", ErrEdgeNotFound, id)
	}

	delete(db.edges, id)
	return edge, nil
}

func (db *ladybugDB) GetEdge(ctx context.Context, id, branch string) (*Edge, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		edge, ok := br.edges[id]
		if !ok {
			return nil, fmt.Errorf("%w: edge with id %q", ErrEdgeNotFound, id)
		}
		e := *edge
		return &e, nil
	}

	edge, ok := db.edges[id]
	if !ok {
		return nil, fmt.Errorf("%w: edge with id %q", ErrEdgeNotFound, id)
	}

	e := *edge
	if e.Properties != nil {
		props := make(map[string]string, len(e.Properties))
		maps.Copy(props, e.Properties)
		e.Properties = props
	}
	return &e, nil
}

func (db *ladybugDB) ListEdgesOfType(ctx context.Context, edgeType, branch string) ([]Edge, error) {
	// ponytail: Unlike ListEntities (which ignores branch entirely), this method
	// actually routes non-empty non-"main" branches to br.edges. Treat "main" and
	// "" as the main DB.
	if branch != "" && branch != "main" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return nil, fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		var results []Edge
		for _, e := range br.edges {
			if e.Type == edgeType {
				results = append(results, *e)
			}
		}
		return results, nil
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	var results []Edge
	for _, e := range db.edges {
		if e.Type == edgeType {
			results = append(results, *e)
		}
	}
	return results, nil
}

// --- Query methods ---

func (db *ladybugDB) ExecuteCypher(ctx context.Context, cypher string, params map[string]any, branch string) ([]map[string]any, error) {
	if cypher == "" {
		return nil, ErrEmptyQuery
	}

	// ponytail: mutation/DDL detection uses a naive first-word heuristic that can be
	// defeated by comments, string literals, or whitespace tricks.
	upper := strings.TrimSpace(cypher)
	firstWord := extractFirstWord(upper)

	switch firstWord {
	case "CREATE", "DELETE", "SET", "MERGE", "REMOVE", "DROP", "FOREACH":
		return nil, ErrMutationCypher
	case "":
		return nil, ErrInvalidCypher
	}

	if firstWord == "MATCH" || firstWord == "RETURN" || firstWord == "WITH" || firstWord == "UNWIND" || firstWord == "CALL" || firstWord == "LOAD" {
		if branch != "" {
			db.mu.Lock()
			br, ok := db.branches[branch]
			if !ok {
				db.mu.Unlock()
				return nil, fmt.Errorf("branch %q not found", branch)
			}
			br.mu.Lock()
			db.mu.Unlock()
			result := db.executeMatchOnBranch(br, cypher)
			br.mu.Unlock()
			return result, nil
		}
		rows := db.executeMatch(cypher)
		return rows, nil
	}

	return nil, ErrInvalidCypher
}

// executeMatchOnBranch runs a simplified MATCH query against a branch DB.
func (db *ladybugDB) executeMatchOnBranch(br *branchDB, cypher string) []map[string]any {
	entityType := extractEntityType(cypher)
	var results []map[string]any
	for _, entity := range br.entities {
		if entityType != "" && entity.Type != entityType {
			continue
		}
		row := map[string]any{
			"id":         entity.Id,
			"type":       entity.Type,
			"properties": entity.Properties,
		}
		results = append(results, row)
	}
	return results
}

// extractFirstWord returns the first word of the query (uppercased).
func extractFirstWord(query string) string {
	query = strings.TrimSpace(query)
	if idx := strings.IndexAny(query, " \t\n\r("); idx >= 0 {
		return strings.ToUpper(query[:idx])
	}
	return strings.ToUpper(query)
}

// executeMatch executes a simplified MATCH query against the in-memory store.
// ponytail: This is a minimal implementation sufficient for tests. It handles
// only "MATCH (n:Type) RETURN n" patterns and similar simple queries.
// Full Cypher parsing is deferred to the LadybugDB-backed implementation.
func (db *ladybugDB) executeMatch(cypher string) []map[string]any {
	// Extract entity type from MATCH clause
	entityType := extractEntityType(cypher)

	// Extract property filters from WHERE clause
	filters := extractWhereFilters(cypher)

	var results []map[string]any

	for _, entity := range db.entities {
		if entityType != "" && entity.Type != entityType {
			continue
		}

		// Apply WHERE filters
		if !matchFilters(entity, filters) {
			continue
		}

		row := map[string]any{
			"id":         entity.Id,
			"type":       entity.Type,
			"properties": entity.Properties,
		}
		results = append(results, row)
	}

	// Apply LIMIT
	limit := extractLimit(cypher)
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results
}

// extractEntityType extracts the entity type from a MATCH clause.
func extractEntityType(cypher string) string {
	_, after, ok := strings.Cut(cypher, "MATCH")
	if !ok {
		return ""
	}
	rest := after
	// Find the pattern (n:Type)
	_, after, ok = strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	afterColon := after
	// Read until closing paren or space
	end := strings.IndexAny(afterColon, ") \t\n\r")
	if end < 0 {
		return strings.TrimSpace(afterColon)
	}
	return strings.TrimSpace(afterColon[:end])
}

// extractWhereFilters extracts simple equality filters from a WHERE clause.
func extractWhereFilters(cypher string) map[string]string {
	filters := make(map[string]string)
	_, after, ok := strings.Cut(cypher, "WHERE")
	if !ok {
		return filters
	}
	rest := after
	// Find the LIMIT or end
	limitIdx := strings.Index(rest, "LIMIT")
	if limitIdx < 0 {
		limitIdx = len(rest)
	}
	whereClause := strings.TrimSpace(rest[:limitIdx])
	// Parse simple n.prop = 'value' patterns
	parts := strings.SplitSeq(whereClause, "AND")
	for part := range parts {
		part = strings.TrimSpace(part)
		eqIdx := strings.Index(part, "=")
		if eqIdx < 0 {
			continue
		}
		// Extract after the dot
		dotIdx := strings.Index(part[:eqIdx], ".")
		if dotIdx < 0 {
			continue
		}
		propName := strings.TrimSpace(part[dotIdx+1 : eqIdx])
		rawVal := strings.TrimSpace(part[eqIdx+1:])
		// Remove quotes
		rawVal = strings.Trim(rawVal, "'\"")
		filters[propName] = rawVal
	}
	return filters
}

// matchFilters checks if entity properties match the given filters.
func matchFilters(entity *Entity, filters map[string]string) bool {
	for k, v := range filters {
		if entity.Properties[k] != v {
			return false
		}
	}
	return true
}

// extractLimit extracts the LIMIT value from a Cypher query.
func extractLimit(cypher string) int {
	_, after, ok := strings.Cut(cypher, "LIMIT")
	if !ok {
		return 0
	}
	rest := strings.TrimSpace(after)
	end := strings.IndexAny(rest, " \t\n\r")
	if end < 0 {
		end = len(rest)
	}
	var limit int
	_, _ = fmt.Sscanf(rest[:end], "%d", &limit)
	return limit
}

func (db *ladybugDB) SearchNeighbors(ctx context.Context, embedding []float32, entityType string, topK int, branch string) ([]NeighborResult, error) {
	// ponytail: branch parameter accepted but ignored; SPEC R2 requires read-path methods
	// with an optional transactionId to scope operations to that transaction's isolated
	// branch instance. Branch routing deferred.
	// Validate embedding
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, ErrNaNOrInfEmbedding
		}
	}
	if len(embedding) == 0 {
		return nil, ErrEmbeddingRequired
	}

	if topK < 0 {
		return nil, ErrInvalidTopK
	}
	if topK == 0 {
		topK = 10
	}

	// Validate entity type
	if entityType != "" {
		def, ok := db.entityTypeDefs[entityType]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownEntityType, entityType)
		}
		if !def.EnableVectorIndex {
			return nil, fmt.Errorf("%w: %q", ErrNonIndexedType, entityType)
		}
		// Check dimension
		if dim, bootstrapped := db.vecDimension[entityType]; bootstrapped {
			if len(embedding) != dim {
				return nil, fmt.Errorf("%w: expected dimension %d, got %d", ErrEmbeddingDimension, dim, len(embedding))
			}
		} else {
			// Index not bootstrapped: no results
			return []NeighborResult{}, nil
		}
	}

	var results []NeighborResult

	for _, entity := range db.entities {
		if entityType != "" && entity.Type != entityType {
			continue
		}
		if len(entity.Embedding) == 0 {
			continue
		}

		// Validate dimension match
		if len(entity.Embedding) != len(embedding) {
			continue
		}

		// Compute cosine similarity distance
		dist := cosineDistance(embedding, entity.Embedding)
		results = append(results, NeighborResult{
			Entity:   *entity,
			Distance: dist,
		})
	}

	// Sort by distance (ascending)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Distance < results[j].Distance
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

func cosineDistance(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 1.0
	}
	return 1.0 - (dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

func (db *ladybugDB) FullTextSearch(ctx context.Context, query, entityType string, branch string) ([]Entity, error) {
	// ponytail: branch parameter accepted but ignored; SPEC R2 requires read-path methods
	// with an optional transactionId to scope operations to that transaction's isolated
	// branch instance. Branch routing deferred.
	if query == "" {
		return nil, ErrEmptyQuery
	}

	if entityType != "" {
		if _, ok := db.entityTypeDefs[entityType]; !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownEntityType, entityType)
		}
	}

	var results []Entity
	for _, entity := range db.entities {
		if entityType != "" && entity.Type != entityType {
			continue
		}
		// Search all string properties
		matched := false
		for _, val := range entity.Properties {
			if strings.Contains(strings.ToLower(val), strings.ToLower(query)) {
				matched = true
				break
			}
		}
		if matched {
			results = append(results, *entity)
		}
	}

	return results, nil
}

func (db *ladybugDB) ListEntities(ctx context.Context, entityType string, pageSize int, pageToken string, branch string) ([]Entity, string, error) {
	// ponytail: branch parameter accepted but ignored; SPEC R2 requires read-path methods
	// with an optional transactionId to scope operations to that transaction's isolated
	// branch instance. Branch routing deferred.
	if pageSize < 0 {
		return nil, "", ErrInvalidPageSize
	}
	if pageSize == 0 {
		pageSize = 1000
	}
	if pageSize > 1000 {
		return nil, "", fmt.Errorf("%w: page size %d exceeds maximum of 1000", ErrInvalidPageSize, pageSize)
	}

	def, ok := db.entityTypeDefs[entityType]
	if !ok {
		return nil, "", fmt.Errorf("%w: %q", ErrUnknownEntityType, entityType)
	}
	_ = def // type exists

	// Decode page token
	var lastSeenID string
	if pageToken != "" {
		data, err := base64.StdEncoding.DecodeString(pageToken)
		if err != nil {
			return nil, "", fmt.Errorf("%w: malformed page token", ErrInvalidPageToken)
		}
		lastSeenID = string(data)
		if err := validateUUID(lastSeenID); err != nil {
			return nil, "", fmt.Errorf("%w: malformed page token", ErrInvalidPageToken)
		}
		// Check if the cursor entity exists
		if _, exists := db.entities[lastSeenID]; !exists {
			return nil, "", fmt.Errorf("%w: cursor references non-existent entity", ErrInvalidPageToken)
		}
	}

	// Gather all entities of the requested type
	var allEntities []*Entity
	for _, e := range db.entities {
		if e.Type == entityType {
			allEntities = append(allEntities, e)
		}
	}

	// Sort by ID for deterministic ordering
	sort.Slice(allEntities, func(i, j int) bool {
		return allEntities[i].Id < allEntities[j].Id
	})

	// Find start index
	startIdx := 0
	if lastSeenID != "" {
		for i, e := range allEntities {
			if e.Id == lastSeenID {
				startIdx = i + 1
				break
			}
		}
	}

	if startIdx >= len(allEntities) {
		return []Entity{}, "", nil
	}

	endIdx := min(startIdx+pageSize, len(allEntities))

	page := allEntities[startIdx:endIdx]

	// Convert to value type
	result := make([]Entity, len(page))
	for i, e := range page {
		result[i] = *e
	}

	// Generate next page token
	var nextToken string
	if endIdx < len(allEntities) {
		lastEntity := allEntities[endIdx-1]
		nextToken = base64.StdEncoding.EncodeToString([]byte(lastEntity.Id))
	}

	return result, nextToken, nil
}

// --- Rule methods ---

func (db *ladybugDB) ValidateEdgeRules(sourceType, targetType, edgeType string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.validateEdgeRulesLocked(sourceType, targetType, edgeType)
}

func (db *ladybugDB) validateEdgeRulesLocked(sourceType, targetType, edgeType string) error {
	// Validate types exist
	if _, ok := db.entityTypeDefs[sourceType]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEntityType, sourceType)
	}
	if _, ok := db.entityTypeDefs[targetType]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEntityType, targetType)
	}
	if _, ok := db.edgeTypeDefs[edgeType]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEdgeType, edgeType)
	}

	rules, ok := db.ruleIndex[sourceType]
	if !ok || len(rules) == 0 {
		return fmt.Errorf("%w: entity type %q does not permit any edges", ErrEdgeRuleViolation, sourceType)
	}

	// OR over entries
	for _, rule := range rules {
		// Within entry: AND between canConnectTo and using
		targetMatch := slices.Contains(rule.CanConnectTo, targetType)

		edgeMatch := slices.Contains(rule.Using, edgeType)

		if targetMatch && edgeMatch {
			return nil
		}
	}

	return fmt.Errorf("%w: entity type %q does not permit connection to %q via %q", ErrEdgeRuleViolation, sourceType, targetType, edgeType)
}

func (br *branchDB) validateEdgeRulesLocked(sourceType, targetType, edgeType string) error {
	if _, ok := br.entityTypeDefs[sourceType]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEntityType, sourceType)
	}
	if _, ok := br.entityTypeDefs[targetType]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEntityType, targetType)
	}
	if _, ok := br.edgeTypeDefs[edgeType]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownEdgeType, edgeType)
	}

	rules, ok := br.ruleIndex[sourceType]
	if !ok || len(rules) == 0 {
		return fmt.Errorf("%w: entity type %q does not permit any edges", ErrEdgeRuleViolation, sourceType)
	}

	for _, rule := range rules {
		targetMatch := slices.Contains(rule.CanConnectTo, targetType)
		edgeMatch := slices.Contains(rule.Using, edgeType)
		if targetMatch && edgeMatch {
			return nil
		}
	}

	return fmt.Errorf("%w: entity type %q does not permit connection to %q via %q", ErrEdgeRuleViolation, sourceType, targetType, edgeType)
}

func (db *ladybugDB) ResolveEntityType(ctx context.Context, entityID, branch string) (string, error) {
	if err := validateUUID(entityID); err != nil {
		return "", err
	}

	if branch != "" {
		db.mu.Lock()
		br, ok := db.branches[branch]
		if !ok {
			db.mu.Unlock()
			return "", fmt.Errorf("branch %q not found", branch)
		}
		br.mu.Lock()
		db.mu.Unlock()
		defer br.mu.Unlock()
		entity, ok := br.entities[entityID]
		if !ok {
			return "", fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, entityID)
		}
		return entity.Type, nil
	}

	entity, ok := db.entities[entityID]
	if !ok {
		return "", fmt.Errorf("%w: entity with id %q", ErrEntityNotFound, entityID)
	}

	return entity.Type, nil
}

// --- Transaction / Branch management ---

func (db *ladybugDB) CreateBranchDB(txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if _, ok := db.branches[txID]; ok {
		return fmt.Errorf("%w: branch for tx %q", ErrBranchAlreadyExists, txID)
	}

	db.branches[txID] = &branchDB{
		entities:       make(map[string]*Entity),
		edges:          make(map[string]*Edge),
		entityTypeDefs: make(map[string]*EntityTypeDef),
		edgeTypeDefs:   make(map[string]*EdgeTypeDef),
		ruleIndex:      make(map[string][]*flowv1.ConnectionRule),
		bootstrapped:   make(map[string]bool),
		vecDimension:   make(map[string]int),
	}

	return nil
}

func (db *ladybugDB) DropBranchDB(txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	delete(db.branches, txID)
	return nil
}

func (db *ladybugDB) ReplicateSchemaToBranch(txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	br, ok := db.branches[txID]
	if !ok {
		return fmt.Errorf("branch for tx %q not found", txID)
	}

	// Copy schema from main to branch
	for name, def := range db.entityTypeDefs {
		clone := &EntityTypeDef{
			Name:              def.Name,
			EnableVectorIndex: def.EnableVectorIndex,
			Properties:        append([]PropertyDef{}, def.Properties...),
		}
		br.entityTypeDefs[name] = clone
	}
	for name, def := range db.edgeTypeDefs {
		clone := &EdgeTypeDef{
			Name:       def.Name,
			Properties: append([]PropertyDef{}, def.Properties...),
		}
		br.edgeTypeDefs[name] = clone
	}
	for name, rules := range db.ruleIndex {
		clonedRules := make([]*flowv1.ConnectionRule, len(rules))
		for i, r := range rules {
			clonedRules[i] = &flowv1.ConnectionRule{
				CanConnectTo: append([]string{}, r.CanConnectTo...),
				Using:        append([]string{}, r.Using...),
			}
		}
		br.ruleIndex[name] = clonedRules
	}

	return nil
}

func (db *ladybugDB) RehydrateFromBranch(ctx context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	br, ok := db.branches[txID]
	if !ok {
		return fmt.Errorf("branch for tx %q not found", txID)
	}

	br.mu.Lock()
	defer br.mu.Unlock()

	// Full replacement: drop existing data and re-create from branch.
	db.entities = make(map[string]*Entity, len(br.entities))
	maps.Copy(db.entities, br.entities)
	db.edges = make(map[string]*Edge, len(br.edges))
	maps.Copy(db.edges, br.edges)

	// Promote bootstrapped and vecDimension from branch to main (SPEC R7 dimension scope).
	db.bootstrapped = make(map[string]bool, len(br.bootstrapped))
	maps.Copy(db.bootstrapped, br.bootstrapped)
	db.vecDimension = make(map[string]int, len(br.vecDimension))
	maps.Copy(db.vecDimension, br.vecDimension)

	// Promote ruleIndex from branch to main.
	db.ruleIndex = make(map[string][]*flowv1.ConnectionRule, len(br.ruleIndex))
	for name, rules := range br.ruleIndex {
		clonedRules := make([]*flowv1.ConnectionRule, len(rules))
		for i, r := range rules {
			clonedRules[i] = &flowv1.ConnectionRule{
				CanConnectTo: append([]string{}, r.CanConnectTo...),
				Using:        append([]string{}, r.Using...),
			}
		}
		db.ruleIndex[name] = clonedRules
	}

	return nil
}

func (db *ladybugDB) RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	// WipeAll (drop-and-recreate semantics)
	db.entities = make(map[string]*Entity)
	db.edges = make(map[string]*Edge)

	// ponytail: ruleIndex, entityTypeDefs, and edgeTypeDefs survive wipe (not re-read from files).
	// bootstrapped and vecDimension are rebuilt from scratch below as entities are inserted.

	// Rebuild bootstrapped state
	db.bootstrapped = make(map[string]bool)
	db.vecDimension = make(map[string]int)

	// Check directories exist — if not, the git repo is empty (no commits on main).
	// Initialize fresh maps instead of erroring; SPEC R8 requires a fresh main.lbug.
	entInfo, err := os.Stat(entitiesDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Empty repo — no entities to read
		} else {
			return err
		}
	} else if !entInfo.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", ErrInvalidEntityDir, entitiesDir)
	} else {
		// Read entity files
		if err := db.readEntitiesFromDir(entitiesDir); err != nil {
			return err
		}
	}

	edgeInfo, err := os.Stat(edgesDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Empty repo — no edges to read
			return nil
		}
		return err
	}
	if !edgeInfo.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", ErrInvalidEdgeDir, edgesDir)
	}

	// Read edge files
	return db.readEdgesFromDir(edgesDir)
}

func (db *ladybugDB) readEntitiesFromDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// If dir doesn't exist, that's OK (no entities to read)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()

		// Skip unknown types silently, but only when schema has been applied.
		// Without applied schema (e.g. during corruption recovery before ApplySchema),
		// load all entity type directories to avoid data loss.
		if len(db.entityTypeDefs) > 0 {
			if _, ok := db.entityTypeDefs[typeName]; !ok {
				continue
			}
		}

		typeDir := filepath.Join(dir, typeName)
		files, err := os.ReadDir(typeDir)
		if err != nil {
			// ponytail: swallow ReadDir error — skips unreadable type subdirectory
			// rather than failing the entire hydration. Masks permission errors and
			// corrupted directory entries; upgrade path: log and skip, do not halt.
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if !strings.HasSuffix(f.Name(), ".json") {
				continue
			}

			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				// ponytail: swallow ReadFile error — skips a single unreadable entity
				// file rather than failing the entire directory. Masks I/O errors and
				// data-loss conditions; upgrade path: log and continue.
				continue
			}

			var jsonEntity struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &jsonEntity); err != nil {
				return fmt.Errorf("%w: unparseable entity file %q: %v", ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), err)
			}

			if jsonEntity.Type == "" {
				return fmt.Errorf("%w: entity file %q is missing required key %q", ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), "type")
			}

			if jsonEntity.ID == "" {
				jsonEntity.ID = uuid.New().String()
			}

			props := jsonEntity.Properties
			if props == nil {
				props = make(map[string]string)
			}

			now := time.Now().UTC()
			entity := &Entity{
				Id:         jsonEntity.ID,
				Type:       jsonEntity.Type,
				Properties: props,
				CreatedAt:  now,
				UpdatedAt:  now,
			}

			db.entities[entity.Id] = entity

			// Entity files don't carry embeddings - skip bootstrapping here.
			_ = db.entityTypeDefs[typeName]
		}
	}

	return nil
}

func (db *ladybugDB) readEdgesFromDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()

		// Skip unknown types silently, but only when schema has been applied.
		// Without applied schema (e.g. during corruption recovery before ApplySchema),
		// load all edge type directories to avoid data loss.
		if len(db.edgeTypeDefs) > 0 {
			if _, ok := db.edgeTypeDefs[typeName]; !ok {
				continue
			}
		}

		typeDir := filepath.Join(dir, typeName)
		files, err := os.ReadDir(typeDir)
		if err != nil {
			// ponytail: swallow ReadDir error — skips unreadable type subdirectory
			// rather than failing the entire edge hydration. Masks permission errors
			// and corrupted directory entries; upgrade path: log and skip.
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if !strings.HasSuffix(f.Name(), ".json") {
				continue
			}

			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				// ponytail: swallow ReadFile error — skips a single unreadable edge
				// file rather than failing the entire directory. Masks I/O errors and
				// data-loss conditions; upgrade path: log and continue.
				continue
			}

			var jsonEdge struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				From       string            `json:"from"`
				To         string            `json:"to"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &jsonEdge); err != nil {
				return fmt.Errorf("%w: unparseable edge file %q: %v", ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), err)
			}

			if jsonEdge.Type == "" || jsonEdge.From == "" || jsonEdge.To == "" {
				return fmt.Errorf("%w: edge file %q is missing required key(s)", ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}

			if jsonEdge.ID == "" {
				jsonEdge.ID = uuid.New().String()
			}

			props := jsonEdge.Properties
			if props == nil {
				props = make(map[string]string)
			}

			now := time.Now().UTC()
			edge := &Edge{
				Id:           jsonEdge.ID,
				Type:         jsonEdge.Type,
				FromEntityID: jsonEdge.From,
				ToEntityID:   jsonEdge.To,
				Properties:   props,
				CreatedAt:    now,
				UpdatedAt:    now,
			}

			db.edges[edge.Id] = edge
		}
	}

	return nil
}

func (db *ladybugDB) HydrateBranchFromFiles(ctx context.Context, txID, entitiesDir, edgesDir string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	br, ok := db.branches[txID]
	if !ok {
		return fmt.Errorf("branch for tx %q not found", txID)
	}

	br.mu.Lock()
	defer br.mu.Unlock()

	// Check directories exist
	if _, err := os.Stat(entitiesDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %q", ErrInvalidEntityDir, entitiesDir)
	}
	if _, err := os.Stat(edgesDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %q", ErrInvalidEdgeDir, edgesDir)
	}

	// Read entity files into branch
	entries, err := os.ReadDir(entitiesDir)
	if err != nil {
		return fmt.Errorf("%w: reading entities directory: %v", ErrInvalidEntityDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		if _, ok := br.entityTypeDefs[typeName]; !ok {
			continue
		}

		typeDir := filepath.Join(entitiesDir, typeName)
		files, err := os.ReadDir(typeDir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}

			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				continue
			}

			var jsonEntity struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &jsonEntity); err != nil {
				return fmt.Errorf("%w: unparseable entity file %q: %v", ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), err)
			}

			if jsonEntity.Type == "" {
				return fmt.Errorf("%w: entity file %q is missing required key %q", ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), "type")
			}

			if jsonEntity.ID == "" {
				jsonEntity.ID = uuid.New().String()
			}
			props := jsonEntity.Properties
			if props == nil {
				props = make(map[string]string)
			}

			now := time.Now().UTC()
			br.entities[jsonEntity.ID] = &Entity{
				Id:         jsonEntity.ID,
				Type:       jsonEntity.Type,
				Properties: props,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
		}
	}

	// Read edge files into branch
	edgeEntries, err := os.ReadDir(edgesDir)
	if err != nil {
		return fmt.Errorf("%w: reading edges directory: %v", ErrInvalidEdgeDir, err)
	}

	for _, entry := range edgeEntries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		if _, ok := br.edgeTypeDefs[typeName]; !ok {
			continue
		}

		typeDir := filepath.Join(edgesDir, typeName)
		files, err := os.ReadDir(typeDir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}

			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				continue
			}

			var jsonEdge struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				From       string            `json:"from"`
				To         string            `json:"to"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &jsonEdge); err != nil {
				return fmt.Errorf("%w: unparseable edge file %q: %v", ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), err)
			}

			if jsonEdge.Type == "" || jsonEdge.From == "" || jsonEdge.To == "" {
				return fmt.Errorf("%w: edge file %q is missing required key(s)", ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}

			if jsonEdge.ID == "" {
				jsonEdge.ID = uuid.New().String()
			}
			props := jsonEdge.Properties
			if props == nil {
				props = make(map[string]string)
			}

			now := time.Now().UTC()
			br.edges[jsonEdge.ID] = &Edge{
				Id:           jsonEdge.ID,
				Type:         jsonEdge.Type,
				FromEntityID: jsonEdge.From,
				ToEntityID:   jsonEdge.To,
				Properties:   props,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
		}
	}

	return nil
}

func (db *ladybugDB) IsVectorIndexBootstrapped(entityType, dbName string) bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	if dbName == "main" || dbName == "" {
		return db.bootstrapped[entityType]
	}
	// Branch check
	br, ok := db.branches[dbName]
	if !ok {
		return false
	}
	return br.bootstrapped[entityType]
}

func (db *ladybugDB) GetEstablishedDimension(entityType, dbName string) (int, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if dbName == "main" || dbName == "" {
		if dim, ok := db.vecDimension[entityType]; ok {
			return dim, nil
		}
		return 0, nil
	}
	br, ok := db.branches[dbName]
	if !ok {
		return 0, nil
	}
	if dim, ok := br.vecDimension[entityType]; ok {
		return dim, nil
	}
	return 0, nil
}

// --- Wipe / Health ---

func (db *ladybugDB) WipeAll(ctx context.Context) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.entities = make(map[string]*Entity)
	db.edges = make(map[string]*Edge)
	db.ruleIndex = make(map[string][]*flowv1.ConnectionRule)
	db.bootstrapped = make(map[string]bool)
	db.vecDimension = make(map[string]int)

	return nil
}

func (db *ladybugDB) HasOpenTransactions() bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	return len(db.branches) > 0
}

func (db *ladybugDB) Health(ctx context.Context) (*HealthResult, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	result := &HealthResult{
		LadybugOK:     !db.closed,
		SchemaApplied: len(db.entityTypeDefs) > 0 || len(db.edgeTypeDefs) > 0,
		PVCWritable:   true, // ponytail: stub always reports PVC writable
	}

	return result, nil
}

// --- Branch scanning ---

func (db *ladybugDB) DumpAllEntities(ctx context.Context, txID string) ([]Entity, error) {
	db.mu.Lock()
	br, ok := db.branches[txID]
	if !ok {
		db.mu.Unlock()
		return nil, fmt.Errorf("branch for tx %q not found", txID)
	}
	br.mu.Lock()
	db.mu.Unlock()
	defer br.mu.Unlock()

	entities := make([]Entity, 0, len(br.entities))
	for _, e := range br.entities {
		entities = append(entities, *e)
	}
	return entities, nil
}

func (db *ladybugDB) DumpAllEdges(ctx context.Context, txID string) ([]Edge, error) {
	db.mu.Lock()
	br, ok := db.branches[txID]
	if !ok {
		db.mu.Unlock()
		return nil, fmt.Errorf("branch for tx %q not found", txID)
	}
	br.mu.Lock()
	db.mu.Unlock()
	defer br.mu.Unlock()

	edges := make([]Edge, 0, len(br.edges))
	for _, e := range br.edges {
		edges = append(edges, *e)
	}
	return edges, nil
}

func (db *ladybugDB) ListEntityTypes(txID string) ([]string, error) {
	db.mu.Lock()
	br, ok := db.branches[txID]
	if !ok {
		db.mu.Unlock()
		return nil, fmt.Errorf("branch for tx %q not found", txID)
	}
	br.mu.Lock()
	db.mu.Unlock()
	defer br.mu.Unlock()

	names := make([]string, 0, len(br.entityTypeDefs))
	for name := range br.entityTypeDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// --- Validation helpers ---

// validateUUID validates that the given string is a valid UUID v4.
func validateUUID(id string) error {
	_, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidIDFormat, id)
	}
	return nil
}
