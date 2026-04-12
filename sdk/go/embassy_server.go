package flow

import (
	"context"
	"fmt"
	"io"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

const builtInLawPetitionImportType = "law-petition"

// EmbassyFlowImportTypeSpec mirrors the flow-authored import type inputs the
// Embassy SDK needs for resolution and materialisation scaffolding.
type EmbassyFlowImportTypeSpec struct {
	Node                 string
	RequireForeignStamps map[string][]string
}

// EmbassyResolvedImportType describes one effective import type after merging
// built-in system types with flow-authored types.
type EmbassyResolvedImportType struct {
	Name    string
	BuiltIn bool
	Spec    *EmbassyFlowImportTypeSpec
}

// EmbassyServiceHandler is the server-side Embassy scaffold interface.
type EmbassyServiceHandler interface {
	PreflightManifest(context.Context, *flowv1.PreflightManifestRequest) (*flowv1.PreflightManifestResponse, error)
	StreamPackage(context.Context, []*flowv1.PackageChunk) (*flowv1.StreamPackageResponse, error)
	ExportPackage(context.Context, *flowv1.ExportPackageRequest) ([]*flowv1.PackageChunk, error)
}

// EmbassyPackageStager stages streamed package chunks before materialisation.
type EmbassyPackageStager interface {
	StageManifest(context.Context, *flowv1.TransferManifest) error
	StageChunk(context.Context, *flowv1.PackageChunk) error
	Complete(context.Context) (*EmbassyStagedPackage, error)
}

// EmbassyMaterializer materialises a staged import package into a local result.
type EmbassyMaterializer interface {
	MaterializeImport(
		context.Context,
		EmbassyResolvedImportType,
		*EmbassyStagedPackage,
	) (*flowv1.StreamPackageResponse, error)
}

// EmbassyStagedPackage holds a manifest and its staged transfer chunks.
type EmbassyStagedPackage struct {
	Manifest *flowv1.TransferManifest
	Chunks   []*flowv1.PackageChunk
}

type embassyServiceServer struct {
	flowv1.UnimplementedEmbassyServiceServer
	handler EmbassyServiceHandler
}

// NewEmbassyServer adapts an EmbassyServiceHandler to the generated gRPC server.
func NewEmbassyServer(handler EmbassyServiceHandler) flowv1.EmbassyServiceServer {
	return &embassyServiceServer{handler: handler}
}

func (s *embassyServiceServer) PreflightManifest(
	ctx context.Context, req *flowv1.PreflightManifestRequest,
) (*flowv1.PreflightManifestResponse, error) {
	if s.handler == nil {
		return nil, fmt.Errorf("flow sdk: embassy server: no handler configured")
	}
	return s.handler.PreflightManifest(ctx, req)
}

func (s *embassyServiceServer) StreamPackage(stream flowv1.EmbassyService_StreamPackageServer) error {
	if s.handler == nil {
		return fmt.Errorf("flow sdk: embassy server: no handler configured")
	}

	var chunks []*flowv1.PackageChunk
	for {
		chunk, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		chunks = append(chunks, chunk)
	}

	resp, err := s.handler.StreamPackage(stream.Context(), chunks)
	if err != nil {
		return err
	}
	return stream.SendAndClose(resp)
}

func (s *embassyServiceServer) ExportPackage(
	req *flowv1.ExportPackageRequest, stream flowv1.EmbassyService_ExportPackageServer,
) error {
	if s.handler == nil {
		return fmt.Errorf("flow sdk: embassy server: no handler configured")
	}

	chunks, err := s.handler.ExportPackage(stream.Context(), req)
	if err != nil {
		return err
	}

	for _, chunk := range chunks {
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}

	return nil
}

// DefaultSystemImportTypes returns the built-in import types known to the SDK.
func DefaultSystemImportTypes() map[string]EmbassyResolvedImportType {
	return map[string]EmbassyResolvedImportType{
		builtInLawPetitionImportType: {
			Name:    builtInLawPetitionImportType,
			BuiltIn: true,
		},
	}
}

// ResolveEmbassyImportType resolves an import type against the merged effective
// namespace of built-in system import types and flow-authored import types.
func ResolveEmbassyImportType(
	name string,
	system map[string]EmbassyResolvedImportType,
	flowImportTypes map[string]EmbassyFlowImportTypeSpec,
) (EmbassyResolvedImportType, bool) {
	if resolved, ok := system[name]; ok {
		return resolved, true
	}

	if spec, ok := flowImportTypes[name]; ok {
		specCopy := spec
		return EmbassyResolvedImportType{Name: name, Spec: &specCopy}, true
	}

	return EmbassyResolvedImportType{}, false
}

// MaterializeStreamedPackage stages incoming chunks then hands the staged
// package to the materializer with the resolved import type.
func MaterializeStreamedPackage(
	ctx context.Context,
	stager EmbassyPackageStager,
	materializer EmbassyMaterializer,
	importType EmbassyResolvedImportType,
	chunks []*flowv1.PackageChunk,
) (*flowv1.StreamPackageResponse, error) {
	for _, chunk := range chunks {
		if manifest := chunk.GetManifest(); manifest != nil {
			if err := stager.StageManifest(ctx, manifest); err != nil {
				return nil, err
			}
		}
		if err := stager.StageChunk(ctx, chunk); err != nil {
			return nil, err
		}
	}

	staged, err := stager.Complete(ctx)
	if err != nil {
		return nil, err
	}

	return materializer.MaterializeImport(ctx, importType, staged)
}
