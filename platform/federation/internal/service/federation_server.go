// Package service implements the FederationService gRPC server.
//
// The Federation service is the control-plane authority for Flow federations.
// It manages membership, endpoint discovery, authority publisher roles,
// published law distribution, and petition-outcome events.
//
// All persistent state lives in Kubernetes CRDs (FederationMember,
// FederationState) backed by etcd -- no SQLite.
package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	federationv1 "github.com/gideas/flow/federation/api/v1"
)

// LibrarianDialer abstracts dialing a remote Librarian so tests can inject
// a spy/mock without real gRPC connections.
type LibrarianDialer interface {
	// DialLibrarian connects to a Librarian at the given address and returns
	// a client plus a closer function. The caller must call the closer when
	// done with the client.
	DialLibrarian(ctx context.Context, address string) (flowv1.LibrarianServiceClient, func() error, error)
}

// EventDispatcher abstracts the mechanism for broadcasting events to
// subscribers. The subscriber registry (13.9.1) provides the production
// implementation; tests inject a spy.
type EventDispatcher interface {
	// DispatchLawEvent sends a PublishedLawEvent to all relevant subscribers.
	// publisherStateRefs identifies the states the publishing Flow belongs to,
	// used for state-level filtering (only subscribers sharing a state receive
	// the event). For federation-level publications all subscribers receive
	// the event regardless of publisherStateRefs.
	DispatchLawEvent(ctx context.Context, event *flowv1.PublishedLawEvent, publisherStateRefs []string)
	// DispatchPetitionOutcomeEvent sends a PetitionOutcomeEvent to the
	// originating subscriber Flow.
	DispatchPetitionOutcomeEvent(ctx context.Context, event *flowv1.PetitionOutcomeEvent)
}

// ConflictAnalyser abstracts the LLM-based conflict analysis so tests can
// inject a deterministic stub. The production implementation uses an SDK
// Agent with Ollama to evaluate semantic matches against the candidate law.
type ConflictAnalyser interface {
	// AnalyseConflicts evaluates whether any of the semantically similar
	// laws actually conflict with the candidate law. It returns a report
	// indicating whether conflicts were found and, if so, which laws
	// conflict and what remediation is suggested.
	AnalyseConflicts(
		ctx context.Context,
		candidateLaw *flowv1.Law,
		similarLaws []*flowv1.SimilarLaw,
	) (*ConflictReport, error)
}

// ConflictReport holds the result of LLM-based conflict analysis.
type ConflictReport struct {
	// HasConflicts is true when the analyser determines that at least one
	// similar law actually conflicts with the candidate.
	HasConflicts bool
	// ConflictingLawIDs lists the IDs of laws that conflict.
	ConflictingLawIDs []string
	// RemediationText is a human-readable description of the conflicts
	// and suggested remediation actions.
	RemediationText string
}

// GRPCLibrarianDialer is the production implementation that dials a real
// gRPC endpoint.
type GRPCLibrarianDialer struct{}

// DialLibrarian dials a Librarian at addr using insecure credentials (mTLS
// is deferred to a later phase).
func (d *GRPCLibrarianDialer) DialLibrarian(
	ctx context.Context, addr string,
) (flowv1.LibrarianServiceClient, func() error, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial librarian %s: %w", addr, err)
	}
	return flowv1.NewLibrarianServiceClient(conn), conn.Close, nil
}

// FederationServer implements flowv1.FederationServiceServer backed by
// Kubernetes CRDs via a controller-runtime client.
type FederationServer struct {
	flowv1.UnimplementedFederationServiceServer
	k8sClient          client.Client
	namespace          string
	config             *flowv1.FederationConfig
	bootstrapToken     string
	librarianDialer    LibrarianDialer
	conflictAnalyser   ConflictAnalyser
	eventDispatcher    EventDispatcher
	subscriberRegistry *SubscriberRegistry
}

// FederationOption configures a FederationServer.
type FederationOption func(*FederationServer)

// WithFederationConfig sets the federation-wide config returned to joining members.
func WithFederationConfig(cfg *flowv1.FederationConfig) FederationOption {
	return func(s *FederationServer) { s.config = cfg }
}

// WithBootstrapToken sets the expected bootstrap token for authentication.
func WithBootstrapToken(token string) FederationOption {
	return func(s *FederationServer) { s.bootstrapToken = token }
}

// WithLibrarianDialer sets the dialer used to connect to remote Librarians
// for distributed conflict detection during publication admission.
func WithLibrarianDialer(d LibrarianDialer) FederationOption {
	return func(s *FederationServer) { s.librarianDialer = d }
}

// WithConflictAnalyser sets the analyser used for LLM-based conflict
// detection during publication admission.
func WithConflictAnalyser(a ConflictAnalyser) FederationOption {
	return func(s *FederationServer) { s.conflictAnalyser = a }
}

// WithEventDispatcher sets the dispatcher used to broadcast events to
// subscribers (law publication and petition outcome events).
func WithEventDispatcher(d EventDispatcher) FederationOption {
	return func(s *FederationServer) { s.eventDispatcher = d }
}

// NewFederationServer returns a FederationServer backed by the given
// Kubernetes client. A SubscriberRegistry is always created and used as
// the fallback EventDispatcher when no explicit dispatcher is injected
// (e.g. via WithEventDispatcher for testing). The registry is also used
// by SubscribeLawUpdates and SubscribePetitionOutcomes to manage active
// gRPC server streams.
func NewFederationServer(k8sClient client.Client, namespace string, opts ...FederationOption) *FederationServer {
	registry := NewSubscriberRegistry()
	srv := &FederationServer{
		k8sClient:          k8sClient,
		namespace:          namespace,
		subscriberRegistry: registry,
	}
	for _, o := range opts {
		o(srv)
	}
	// If no explicit dispatcher was injected, use the subscriber registry.
	if srv.eventDispatcher == nil {
		srv.eventDispatcher = registry
	}
	return srv
}

// JoinFederation creates a FederationMember CR for the joining Flow and
// returns the federation config, CA, available states, and assigned roles.
func (s *FederationServer) JoinFederation(
	ctx context.Context,
	req *flowv1.JoinFederationRequest,
) (*flowv1.JoinFederationResponse, error) {
	// Validate inputs.
	if req.GetBootstrapToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "bootstrap_token is required")
	}
	if req.GetFlowIdentity() == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_identity is required")
	}
	if req.GetEmbassyEndpoint() == "" {
		return nil, status.Error(codes.InvalidArgument, "embassy_endpoint is required")
	}

	// Authenticate the bootstrap token.
	if req.GetBootstrapToken() != s.bootstrapToken {
		return nil, status.Error(codes.PermissionDenied, "invalid bootstrap token")
	}

	// Build the FederationMember CR. Name is derived from the flow identity
	// using a K8s-safe transformation (lowercase, replace non-alphanumeric
	// with hyphens).
	memberName := toK8sName(req.GetFlowIdentity())
	member := &federationv1.FederationMember{
		ObjectMeta: metav1.ObjectMeta{
			Name:      memberName,
			Namespace: s.namespace,
		},
		Spec: federationv1.FederationMemberSpec{
			FlowIdentity:    req.GetFlowIdentity(),
			EmbassyEndpoint: req.GetEmbassyEndpoint(),
		},
	}

	// Create the CR. If it already exists, return AlreadyExists.
	if err := s.k8sClient.Create(ctx, member); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil, status.Errorf(codes.AlreadyExists, "flow %q is already a federation member", req.GetFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "failed to create FederationMember: %v", err)
	}

	// Read all FederationState CRs to populate the response.
	states, err := s.listStates(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list federation states: %v", err)
	}

	// Build publisher roles from the member spec (initially empty for new
	// members -- roles are assigned by the federation admin via CR update).
	roles := toProtoPublisherRoles(member.Spec.PublisherRoles)

	return &flowv1.JoinFederationResponse{
		IntermediateCaPem: s.config.GetRootCaPem(),
		FederationConfig:  s.config,
		States:            states,
		PublisherRoles:    roles,
	}, nil
}

// LeaveFederation deletes the FederationMember CR for the departing Flow.
func (s *FederationServer) LeaveFederation(
	ctx context.Context,
	req *flowv1.LeaveFederationRequest,
) (*flowv1.LeaveFederationResponse, error) {
	if req.GetFlowIdentity() == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_identity is required")
	}

	memberName := toK8sName(req.GetFlowIdentity())
	member := &federationv1.FederationMember{}
	key := client.ObjectKey{Namespace: s.namespace, Name: memberName}

	if err := s.k8sClient.Get(ctx, key, member); err != nil {
		if errors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "flow %q is not a federation member", req.GetFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "failed to get FederationMember: %v", err)
	}

	if err := s.k8sClient.Delete(ctx, member); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete FederationMember: %v", err)
	}

	return &flowv1.LeaveFederationResponse{Acknowledged: true}, nil
}

// GetMembership returns the current membership snapshot for a Flow,
// resolving state names from FederationState CRs.
func (s *FederationServer) GetMembership(
	ctx context.Context,
	req *flowv1.GetMembershipRequest,
) (*flowv1.GetMembershipResponse, error) {
	if req.GetFlowIdentity() == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_identity is required")
	}

	memberName := toK8sName(req.GetFlowIdentity())
	member := &federationv1.FederationMember{}
	key := client.ObjectKey{Namespace: s.namespace, Name: memberName}

	if err := s.k8sClient.Get(ctx, key, member); err != nil {
		if errors.IsNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "flow %q is not a federation member", req.GetFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "failed to get FederationMember: %v", err)
	}

	// Resolve state names from FederationState CRs for the member's stateRefs.
	states, err := s.resolveStates(ctx, member.Spec.StateRefs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resolve states: %v", err)
	}

	return &flowv1.GetMembershipResponse{
		Member: &flowv1.FederationMember{
			FlowIdentity:    member.Spec.FlowIdentity,
			EmbassyEndpoint: member.Spec.EmbassyEndpoint,
			States:          states,
			PublisherRoles:  toProtoPublisherRoles(member.Spec.PublisherRoles),
		},
	}, nil
}

// DiscoverEndpoints returns the Embassy endpoints for all federation members,
// optionally filtered by state membership. Each FlowEndpoint includes the
// member's flow identity, embassy address, and state IDs.
func (s *FederationServer) DiscoverEndpoints(
	ctx context.Context,
	req *flowv1.DiscoverEndpointsRequest,
) (*flowv1.DiscoverEndpointsResponse, error) {
	// List all FederationMember CRs in the namespace.
	var memberList federationv1.FederationMemberList
	if err := s.k8sClient.List(ctx, &memberList, client.InNamespace(s.namespace)); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list FederationMembers: %v", err)
	}

	stateFilter := req.GetStateFilter()
	endpoints := make([]*flowv1.FlowEndpoint, 0, len(memberList.Items))

	for i := range memberList.Items {
		m := &memberList.Items[i]

		// If a state filter is set, only include members whose stateRefs
		// contain the requested state.
		if stateFilter != "" && !containsState(m.Spec.StateRefs, stateFilter) {
			continue
		}

		endpoints = append(endpoints, &flowv1.FlowEndpoint{
			FlowIdentity:   m.Spec.FlowIdentity,
			EmbassyAddress: m.Spec.EmbassyEndpoint,
			StateIds:       m.Spec.StateRefs,
		})
	}

	return &flowv1.DiscoverEndpointsResponse{Endpoints: endpoints}, nil
}

// GetPetitionTarget resolves the authority Flow that handles petitions for
// a given scope/domain. It lists all FederationMember CRs and returns the
// first member whose publisherRoles contain a matching scope.
func (s *FederationServer) GetPetitionTarget(
	ctx context.Context,
	req *flowv1.GetPetitionTargetRequest,
) (*flowv1.GetPetitionTargetResponse, error) {
	if req.GetScope() == "" {
		return nil, status.Error(codes.InvalidArgument, "scope is required")
	}

	// List all FederationMember CRs in the namespace.
	var memberList federationv1.FederationMemberList
	if err := s.k8sClient.List(ctx, &memberList, client.InNamespace(s.namespace)); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list FederationMembers: %v", err)
	}

	// Find the first member with a publisher role matching the requested scope.
	for i := range memberList.Items {
		m := &memberList.Items[i]
		for _, role := range m.Spec.PublisherRoles {
			if role.Scope == req.GetScope() {
				return &flowv1.GetPetitionTargetResponse{
					AuthorityFlowIdentity: m.Spec.FlowIdentity,
					EmbassyEndpoint:       m.Spec.EmbassyEndpoint,
				}, nil
			}
		}
	}

	return nil, status.Errorf(codes.NotFound, "no authority found for scope %q", req.GetScope())
}

// SubmitPublication validates that the source Flow has authority to publish
// the submitted law, then runs conflict detection (later slices) and either
// accepts or rejects the publication.
func (s *FederationServer) SubmitPublication(
	ctx context.Context,
	req *flowv1.SubmitPublicationRequest,
) (*flowv1.SubmitPublicationResponse, error) {
	// --- Input validation ---
	if req.GetSourceFlowIdentity() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_flow_identity is required")
	}
	if req.GetLaw() == nil {
		return nil, status.Error(codes.InvalidArgument, "law is required")
	}

	// --- Membership check ---
	memberName := toK8sName(req.GetSourceFlowIdentity())
	member := &federationv1.FederationMember{}
	key := client.ObjectKey{Namespace: s.namespace, Name: memberName}
	if err := s.k8sClient.Get(ctx, key, member); err != nil {
		if errors.IsNotFound(err) {
			return nil, status.Errorf(codes.PermissionDenied,
				"flow %q is not a federation member", req.GetSourceFlowIdentity())
		}
		return nil, status.Errorf(codes.Internal, "failed to get FederationMember: %v", err)
	}

	// --- Authority validation ---
	// The member must have at least one publisherRole whose scope matches
	// the submitted law's division.
	lawDivision := req.GetLaw().GetDivision()
	if len(member.Spec.PublisherRoles) == 0 {
		return &flowv1.SubmitPublicationResponse{
			Accepted: false,
			Rejection: &flowv1.PublicationRejection{
				Reason:          flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_UNAUTHORISED,
				RemediationText: fmt.Sprintf("flow %q has no publisher roles", req.GetSourceFlowIdentity()),
			},
		}, nil
	}

	var matchingRole *federationv1.PublisherRoleSpec
	for i := range member.Spec.PublisherRoles {
		if member.Spec.PublisherRoles[i].Scope == lawDivision {
			matchingRole = &member.Spec.PublisherRoles[i]
			break
		}
	}

	if matchingRole == nil {
		return &flowv1.SubmitPublicationResponse{
			Accepted: false,
			Rejection: &flowv1.PublicationRejection{
				Reason: flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_OUT_OF_SCOPE,
				RemediationText: fmt.Sprintf(
					"flow %q has no publisher role for scope %q",
					req.GetSourceFlowIdentity(), lawDivision),
			},
		}, nil
	}

	// State-level publishers must be assigned to at least one state.
	if matchingRole.Level == "state" && len(member.Spec.StateRefs) == 0 {
		return &flowv1.SubmitPublicationResponse{
			Accepted: false,
			Rejection: &flowv1.PublicationRejection{
				Reason: flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_UNAUTHORISED,
				RemediationText: fmt.Sprintf(
					"flow %q has a state-level publisher role for scope %q but is not assigned to any state",
					req.GetSourceFlowIdentity(), lawDivision),
			},
		}, nil
	}

	// --- Authority validation passed ---
	// --- Distributed conflict detection (13.8.2) ---
	// Search publisher Flows' Librarians for semantically similar laws.
	// For state-level publications, only query publishers in the same state(s).
	// For federation-level publications, query all publishers.
	var similarLaws []*flowv1.SimilarLaw
	if s.librarianDialer != nil {
		results, searchErr := s.distributedSearch(ctx, req.GetLaw(), matchingRole, member)
		if searchErr != nil {
			// Search infrastructure error is non-fatal. Log and proceed
			// with empty results -- the analyser will not be called.
			_ = searchErr
		}
		similarLaws = results
	}

	// --- LLM conflict analysis (13.8.3) ---
	// If similar laws were found and a conflict analyser is configured,
	// ask the LLM to determine whether the semantic matches are actual
	// conflicts. Fail-safe: reject on analyser error.
	if len(similarLaws) > 0 && s.conflictAnalyser != nil {
		report, analyseErr := s.conflictAnalyser.AnalyseConflicts(ctx, req.GetLaw(), similarLaws)
		if analyseErr != nil {
			// Fail-safe: do not publish on uncertainty.
			rejection := &flowv1.PublicationRejection{
				Reason:          flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT,
				RemediationText: fmt.Sprintf("conflict analysis failed: %v", analyseErr),
			}
			s.dispatchRejectionOutcome(ctx, req.GetPetitionId(), rejection)
			return &flowv1.SubmitPublicationResponse{
				Accepted:  false,
				Rejection: rejection,
			}, nil
		}
		if report.HasConflicts {
			rejection := &flowv1.PublicationRejection{
				Reason:            flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT,
				ConflictingLawIds: report.ConflictingLawIDs,
				RemediationText:   report.RemediationText,
			}
			s.dispatchRejectionOutcome(ctx, req.GetPetitionId(), rejection)
			return &flowv1.SubmitPublicationResponse{
				Accepted:  false,
				Rejection: rejection,
			}, nil
		}
	}

	// --- Acceptance (13.8.4) ---
	// Determine materialisation tier from the publisher's role level.
	matTier := s.materialisationTier(matchingRole)

	// Build and dispatch the PublishedLawEvent.
	now := timestamppb.Now()
	lawEvent := &flowv1.PublishedLawEvent{
		Law:                   req.GetLaw(),
		MaterialisationTier:   matTier,
		PetitionId:            req.GetPetitionId(),
		PublisherFlowIdentity: req.GetSourceFlowIdentity(),
		PublishedAt:           now,
	}
	if s.eventDispatcher != nil {
		s.eventDispatcher.DispatchLawEvent(ctx, lawEvent, member.Spec.StateRefs)
	}

	// If the law carries a petition_id, dispatch an ACCEPTED PetitionOutcomeEvent.
	if req.GetPetitionId() != "" && s.eventDispatcher != nil {
		s.eventDispatcher.DispatchPetitionOutcomeEvent(ctx, &flowv1.PetitionOutcomeEvent{
			PetitionId:     req.GetPetitionId(),
			Outcome:        flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED,
			PublishedLawId: req.GetLaw().GetId(),
			ResolvedAt:     now,
		})
	}

	return &flowv1.SubmitPublicationResponse{
		Accepted: true,
	}, nil
}

// materialisationTier returns the appropriate law tier for subscriber
// materialisation based on the publisher's role level.
func (s *FederationServer) materialisationTier(role *federationv1.PublisherRoleSpec) flowv1.LawTier {
	switch role.Level {
	case "federation":
		return flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD // Tier 5
	default: // "state" or unrecognised
		return flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION // Tier 4
	}
}

// dispatchRejectionOutcome dispatches a PetitionOutcomeEvent with REJECTED
// status if a petition_id is present and an event dispatcher is configured.
func (s *FederationServer) dispatchRejectionOutcome(
	ctx context.Context,
	petitionID string,
	rejection *flowv1.PublicationRejection,
) {
	if petitionID == "" || s.eventDispatcher == nil {
		return
	}
	s.eventDispatcher.DispatchPetitionOutcomeEvent(ctx, &flowv1.PetitionOutcomeEvent{
		PetitionId: petitionID,
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED,
		Rejection:  rejection,
		ResolvedAt: timestamppb.Now(),
	})
}

// searchResult holds the results from a single Librarian search.
type searchResult struct {
	results []*flowv1.SimilarLaw
	err     error
}

// distributedSearch queries publisher Flows' Librarians in parallel for laws
// semantically similar to the candidate law. Results are consolidated and
// deduplicated by law ID.
func (s *FederationServer) distributedSearch(
	ctx context.Context,
	candidateLaw *flowv1.Law,
	matchingRole *federationv1.PublisherRoleSpec,
	sourceMember *federationv1.FederationMember,
) ([]*flowv1.SimilarLaw, error) {
	// List all FederationMember CRs to find publisher Flows.
	var memberList federationv1.FederationMemberList
	if err := s.k8sClient.List(ctx, &memberList, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list FederationMembers for conflict search: %w", err)
	}

	// Determine which publishers to query based on the role level.
	publishers := s.selectPublishersForSearch(memberList.Items, matchingRole, sourceMember)
	if len(publishers) == 0 {
		return nil, nil
	}

	// Query each publisher's Librarian in parallel.
	var wg sync.WaitGroup
	resultsCh := make(chan searchResult, len(publishers))

	for _, pub := range publishers {
		wg.Add(1)
		go func(endpoint string) {
			defer wg.Done()
			result := s.searchLibrarian(ctx, endpoint, candidateLaw)
			resultsCh <- result
		}(pub.Spec.EmbassyEndpoint)
	}

	wg.Wait()
	close(resultsCh)

	// Consolidate results, deduplicating by law ID.
	return consolidateResults(resultsCh), nil
}

// selectPublishersForSearch returns the FederationMember CRs whose Librarians
// should be queried for conflict detection. State-level publications query only
// publishers sharing a state with the source member. Federation-level
// publications query all publishers.
func (s *FederationServer) selectPublishersForSearch(
	members []federationv1.FederationMember,
	matchingRole *federationv1.PublisherRoleSpec,
	sourceMember *federationv1.FederationMember,
) []*federationv1.FederationMember {
	var selected []*federationv1.FederationMember

	for i := range members {
		m := &members[i]

		// Only consider members with publisher roles.
		if len(m.Spec.PublisherRoles) == 0 {
			continue
		}

		// For state-level publications, only include publishers who share
		// at least one state with the source member.
		if matchingRole.Level == "state" {
			if !sharesState(sourceMember.Spec.StateRefs, m.Spec.StateRefs) {
				continue
			}
		}

		selected = append(selected, m)
	}

	return selected
}

// sharesState reports whether a and b share at least one common state ref.
func sharesState(a, b []string) bool {
	for _, sa := range a {
		if slices.Contains(b, sa) {
			return true
		}
	}
	return false
}

// searchLibrarian dials a single Librarian and calls SearchSimilarLaws.
// Connection and RPC errors are captured in the result (best-effort).
func (s *FederationServer) searchLibrarian(
	ctx context.Context,
	endpoint string,
	candidateLaw *flowv1.Law,
) searchResult {
	libClient, closer, err := s.librarianDialer.DialLibrarian(ctx, endpoint)
	if err != nil {
		return searchResult{err: fmt.Errorf("dial %s: %w", endpoint, err)}
	}
	defer closer() //nolint:errcheck

	resp, err := libClient.SearchSimilarLaws(ctx, &flowv1.SearchSimilarLawsRequest{
		QueryText:   candidateLaw.GetGoal(),
		ScopeFilter: candidateLaw.GetDivision(),
		Limit:       20,
	})
	if err != nil {
		return searchResult{err: fmt.Errorf("search %s: %w", endpoint, err)}
	}
	return searchResult{results: resp.GetResults()}
}

// consolidateResults merges search results from multiple Librarians,
// deduplicating by law ID. When the same law ID appears from multiple
// Librarians, the entry with the highest similarity score is kept.
func consolidateResults(ch <-chan searchResult) []*flowv1.SimilarLaw {
	seen := make(map[string]*flowv1.SimilarLaw)
	for r := range ch {
		if r.err != nil {
			// Best-effort: skip failed searches.
			continue
		}
		for _, sl := range r.results {
			if sl.GetLaw() == nil {
				continue
			}
			lawID := sl.GetLaw().GetId()
			if existing, ok := seen[lawID]; ok {
				// Keep the higher similarity score.
				if sl.GetSimilarityScore() > existing.GetSimilarityScore() {
					seen[lawID] = sl
				}
			} else {
				seen[lawID] = sl
			}
		}
	}

	result := make([]*flowv1.SimilarLaw, 0, len(seen))
	for _, sl := range seen {
		result = append(result, sl)
	}
	return result
}

// containsState reports whether refs contains the given state name.
func containsState(refs []string, state string) bool {
	return slices.Contains(refs, state)
}

// resolveStates looks up FederationState CRs for the given state ref names
// and returns proto State messages with resolved display names.
func (s *FederationServer) resolveStates(ctx context.Context, stateRefs []string) ([]*flowv1.State, error) {
	if len(stateRefs) == 0 {
		return nil, nil
	}

	result := make([]*flowv1.State, 0, len(stateRefs))
	for _, ref := range stateRefs {
		st := &federationv1.FederationState{}
		key := client.ObjectKey{Namespace: s.namespace, Name: ref}
		if err := s.k8sClient.Get(ctx, key, st); err != nil {
			if errors.IsNotFound(err) {
				// State ref points to a non-existent state -- include with
				// empty display name rather than failing the entire request.
				result = append(result, &flowv1.State{
					StateId: ref,
					Name:    "",
				})
				continue
			}
			return nil, fmt.Errorf("get FederationState %q: %w", ref, err)
		}
		result = append(result, &flowv1.State{
			StateId: st.Name,
			Name:    st.Spec.Name,
		})
	}
	return result, nil
}

// listStates retrieves all FederationState CRs in the namespace and
// converts them to proto State messages.
func (s *FederationServer) listStates(ctx context.Context) ([]*flowv1.State, error) {
	var stateList federationv1.FederationStateList
	if err := s.k8sClient.List(ctx, &stateList, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list FederationStates: %w", err)
	}

	result := make([]*flowv1.State, 0, len(stateList.Items))
	for i := range stateList.Items {
		st := &stateList.Items[i]
		result = append(result, &flowv1.State{
			StateId: st.Name,
			Name:    st.Spec.Name,
		})
	}
	return result, nil
}

// toProtoPublisherRoles converts CRD publisher role specs to proto messages.
func toProtoPublisherRoles(specs []federationv1.PublisherRoleSpec) []*flowv1.PublisherRole {
	if len(specs) == 0 {
		return nil
	}
	result := make([]*flowv1.PublisherRole, len(specs))
	for i, spec := range specs {
		result[i] = &flowv1.PublisherRole{
			Scope: spec.Scope,
			Level: spec.Level,
		}
	}
	return result
}

// toK8sName converts a flow identity string to a valid K8s resource name.
// It lowercases the string and replaces non-alphanumeric characters (except
// hyphens and dots) with hyphens.
func toK8sName(identity string) string {
	s := strings.ToLower(identity)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// SubscribeLawUpdates registers a law-update subscriber and streams
// PublishedLawEvents until the client disconnects. The subscriber's
// FederationMember CR is looked up to determine state membership for
// state-level event filtering.
func (s *FederationServer) SubscribeLawUpdates(
	req *flowv1.SubscribeLawUpdatesRequest,
	stream flowv1.FederationService_SubscribeLawUpdatesServer,
) error {
	flowIdentity := req.GetSubscriberFlowIdentity()
	if flowIdentity == "" {
		return status.Error(codes.InvalidArgument, "subscriber_flow_identity is required")
	}

	// Look up the subscriber's FederationMember CR to get state membership.
	memberName := toK8sName(flowIdentity)
	member := &federationv1.FederationMember{}
	key := client.ObjectKey{Namespace: s.namespace, Name: memberName}
	if err := s.k8sClient.Get(stream.Context(), key, member); err != nil {
		if errors.IsNotFound(err) {
			return status.Errorf(codes.NotFound, "flow %q is not a federation member", flowIdentity)
		}
		return status.Errorf(codes.Internal, "failed to get FederationMember: %v", err)
	}

	// Register the subscriber in the registry.
	s.subscriberRegistry.RegisterLawSubscriber(flowIdentity, member.Spec.StateRefs, stream)
	defer s.subscriberRegistry.RemoveLawSubscriber(flowIdentity)

	// Block until the client disconnects.
	<-stream.Context().Done()
	return nil
}

// SubscribePetitionOutcomes registers a petition-outcome subscriber and
// streams PetitionOutcomeEvents until the client disconnects.
func (s *FederationServer) SubscribePetitionOutcomes(
	req *flowv1.SubscribePetitionOutcomesRequest,
	stream flowv1.FederationService_SubscribePetitionOutcomesServer,
) error {
	flowIdentity := req.GetSubscriberFlowIdentity()
	if flowIdentity == "" {
		return status.Error(codes.InvalidArgument, "subscriber_flow_identity is required")
	}

	// Verify the subscriber is a member.
	memberName := toK8sName(flowIdentity)
	member := &federationv1.FederationMember{}
	key := client.ObjectKey{Namespace: s.namespace, Name: memberName}
	if err := s.k8sClient.Get(stream.Context(), key, member); err != nil {
		if errors.IsNotFound(err) {
			return status.Errorf(codes.NotFound, "flow %q is not a federation member", flowIdentity)
		}
		return status.Errorf(codes.Internal, "failed to get FederationMember: %v", err)
	}

	// Register the subscriber in the registry.
	s.subscriberRegistry.RegisterPetitionSubscriber(flowIdentity, stream)
	defer s.subscriberRegistry.RemovePetitionSubscriber(flowIdentity)

	// Block until the client disconnects.
	<-stream.Context().Done()
	return nil
}

// HasLawSubscriber reports whether a law-update subscriber is registered for
// the given flow identity. Exposed for test synchronisation.
func (s *FederationServer) HasLawSubscriber(flowIdentity string) bool {
	return s.subscriberRegistry.HasLawSubscriber(flowIdentity)
}

// HasPetitionSubscriber reports whether a petition-outcome subscriber is
// registered for the given flow identity. Exposed for test synchronisation.
func (s *FederationServer) HasPetitionSubscriber(flowIdentity string) bool {
	return s.subscriberRegistry.HasPetitionSubscriber(flowIdentity)
}
