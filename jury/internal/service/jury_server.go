// Package service implements the Jury gRPC service.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"text/template"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/jury/internal/deliberation"
	"github.com/gideas/flow/jury/internal/jurors"
	flow "github.com/gideas/flow/sdk/go"
)

// maxJurorTypes is the number of distinct juror personality types available.
const maxJurorTypes = 5

// JurorFactory constructs jurors for a deliberation. It abstracts over the
// construction details (Client, Model, Provider) so the server can be tested
// with mock jurors.
type JurorFactory interface {
	// BuildPanel constructs a diverse panel of jurors for the given
	// allowed outcomes and requested jury size.
	BuildPanel(allowedOutcomes []string, jurySize int32) ([]jurors.Juror, error)
}

// JuryServer implements the JuryService gRPC interface.
type JuryServer struct {
	flowv1.UnimplementedJuryServiceServer

	engine  *deliberation.Engine
	factory JurorFactory
}

// NewJuryServer creates a JuryServer with the default deliberation engine
// and the given juror factory.
func NewJuryServer(factory ...JurorFactory) *JuryServer {
	var f JurorFactory
	if len(factory) > 0 {
		f = factory[0]
	}
	return &JuryServer{
		engine:  deliberation.NewEngine(),
		factory: f,
	}
}

// Deliberate implements JuryServiceServer.Deliberate.
func (s *JuryServer) Deliberate(
	ctx context.Context,
	req *flowv1.DeliberateRequest,
) (*flowv1.DeliberateResponse, error) {
	// Validate request.
	if req.GetQuestion() == "" {
		return nil, fmt.Errorf("jury: question must not be empty")
	}
	if len(req.GetAllowedOutcomes()) == 0 {
		return nil, fmt.Errorf("jury: allowed_outcomes must not be empty")
	}
	if req.GetJurySize() <= 0 {
		return nil, fmt.Errorf("jury: jury_size must be positive")
	}
	if req.GetMaxRounds() <= 0 {
		return nil, fmt.Errorf("jury: max_rounds must be positive")
	}
	if s.factory == nil {
		return nil, fmt.Errorf("jury: juror factory not configured")
	}

	slog.Info("jury: deliberation requested",
		"question_len", len(req.GetQuestion()),
		"evidence_len", len(req.GetEvidence()),
		"outcomes", req.GetAllowedOutcomes(),
		"strategy", req.GetConsensusStrategy().String(),
		"max_rounds", req.GetMaxRounds(),
		"jury_size", req.GetJurySize(),
	)

	// Build juror panel.
	panel, err := s.factory.BuildPanel(req.GetAllowedOutcomes(), req.GetJurySize())
	if err != nil {
		return nil, fmt.Errorf("jury: build panel: %w", err)
	}

	// Run deliberation.
	result, err := s.engine.Deliberate(ctx, &deliberation.DeliberationInput{
		Question:          req.GetQuestion(),
		Evidence:          req.GetEvidence(),
		AllowedOutcomes:   req.GetAllowedOutcomes(),
		ConsensusStrategy: req.GetConsensusStrategy(),
		MaxRounds:         req.GetMaxRounds(),
		Panel:             panel,
	})
	if err != nil {
		return nil, fmt.Errorf("jury: deliberation failed: %w", err)
	}

	return &flowv1.DeliberateResponse{
		Outcome:        result.Outcome,
		Justifications: result.Justifications,
		RoundsUsed:     result.RoundsUsed,
		Hung:           result.Hung,
	}, nil
}

// ---------------------------------------------------------------------------
// Juror Type Selection
// ---------------------------------------------------------------------------

// jurorTypeOrder defines the canonical order for diverse panel selection.
// The pool cycles through these types to ensure maximum diversity.
var jurorTypeOrder = []string{
	"textualist",
	"pragmatist",
	"conservator",
	"reformer",
	"devils-advocate",
}

// SelectJurorTypes selects juror type names for a panel, ensuring diversity.
// If jurySize <= 5, each type appears at most once (no duplicates).
// If jurySize > 5, types cycle (ensuring all 5 appear before repeats).
func SelectJurorTypes(jurySize int32) []string {
	types := make([]string, jurySize)
	for i := range jurySize {
		types[i] = jurorTypeOrder[i%int32(maxJurorTypes)]
	}
	return types
}

// ---------------------------------------------------------------------------
// Default Juror Factory (production)
// ---------------------------------------------------------------------------

// JurorConfig holds the optional system prompt override for a juror type.
type JurorConfig struct {
	Name         string `yaml:"name"`
	SystemPrompt string `yaml:"systemPrompt"`
}

// DefaultFactory constructs real jurors backed by flow.Agent instances.
// Each juror uses the same Provider/Model but has a distinct system prompt.
type DefaultFactory struct {
	client  *flow.Client
	model   *flow.Model
	prompts map[string]string // juror type -> optional system prompt override
}

// NewDefaultFactory creates a production juror factory.
func NewDefaultFactory(client *flow.Client, model *flow.Model, configs []JurorConfig) *DefaultFactory {
	prompts := make(map[string]string)
	for _, cfg := range configs {
		if cfg.SystemPrompt != "" {
			prompts[cfg.Name] = cfg.SystemPrompt
		}
	}
	return &DefaultFactory{
		client:  client,
		model:   model,
		prompts: prompts,
	}
}

// BuildPanel constructs a diverse panel of real jurors.
func (f *DefaultFactory) BuildPanel(allowedOutcomes []string, jurySize int32) ([]jurors.Juror, error) {
	// Build shared schema and template.
	schemaBytes, err := jurors.BuildOutputSchema(allowedOutcomes)
	if err != nil {
		return nil, fmt.Errorf("build schema: %w", err)
	}

	queryTmpl, err := jurors.ParseQueryTemplate()
	if err != nil {
		return nil, fmt.Errorf("parse query template: %w", err)
	}

	types := SelectJurorTypes(jurySize)
	panel := make([]jurors.Juror, jurySize)

	for i, typeName := range types {
		prompt := f.prompts[typeName] // empty string uses default
		juror, err := f.createJuror(typeName, prompt, schemaBytes, queryTmpl)
		if err != nil {
			return nil, fmt.Errorf("create juror %s: %w", typeName, err)
		}
		panel[i] = juror
	}

	return panel, nil
}

// createJuror dispatches to the appropriate constructor by type name.
func (f *DefaultFactory) createJuror(
	typeName, systemPrompt string,
	schemaBytes []byte,
	queryTmpl *template.Template,
) (jurors.Juror, error) {
	switch typeName {
	case "textualist":
		return jurors.NewTextualist(f.client, f.model, systemPrompt, schemaBytes, queryTmpl)
	case "pragmatist":
		return jurors.NewPragmatist(f.client, f.model, systemPrompt, schemaBytes, queryTmpl)
	case "conservator":
		return jurors.NewConservator(f.client, f.model, systemPrompt, schemaBytes, queryTmpl)
	case "reformer":
		return jurors.NewReformer(f.client, f.model, systemPrompt, schemaBytes, queryTmpl)
	case "devils-advocate":
		return jurors.NewDevilsAdvocate(f.client, f.model, systemPrompt, schemaBytes, queryTmpl)
	default:
		return nil, fmt.Errorf("unknown juror type: %s", typeName)
	}
}
