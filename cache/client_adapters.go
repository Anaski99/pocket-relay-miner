package cache

import (
	"context"

	"github.com/pokt-network/poktroll/pkg/client"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	prooftypes "github.com/pokt-network/poktroll/x/proof/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// applicationQueryClientAdapter adapts client.ApplicationQueryClient (returns value)
// to ApplicationQueryClient interface (expects pointer).
type applicationQueryClientAdapter struct {
	client client.ApplicationQueryClient
}

func (a *applicationQueryClientAdapter) GetApplication(ctx context.Context, address string) (*apptypes.Application, error) {
	app, err := a.client.GetApplication(ctx, address)
	if err != nil {
		return nil, err
	}
	return &app, nil
}

// NewApplicationQueryClientAdapter creates an adapter for client.ApplicationQueryClient.
func NewApplicationQueryClientAdapter(c client.ApplicationQueryClient) ApplicationQueryClient {
	return &applicationQueryClientAdapter{client: c}
}

// serviceQueryClientAdapter adapts client.ServiceQueryClient (returns value)
// to ServiceQueryClient interface (expects pointer).
type serviceQueryClientAdapter struct {
	client client.ServiceQueryClient
}

func (a *serviceQueryClientAdapter) GetService(ctx context.Context, serviceID string) (*sharedtypes.Service, error) {
	service, err := a.client.GetService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	return &service, nil
}

// NewServiceQueryClientAdapter creates an adapter for client.ServiceQueryClient.
func NewServiceQueryClientAdapter(c client.ServiceQueryClient) ServiceQueryClient {
	return &serviceQueryClientAdapter{client: c}
}

// proofQueryClientAdapter adapts client.ProofQueryClient (returns ProofParams interface)
// to ProofQueryClient interface (expects *prooftypes.Params).
type proofQueryClientAdapter struct {
	client client.ProofQueryClient
}

func (a *proofQueryClientAdapter) GetParams(ctx context.Context) (*prooftypes.Params, error) {
	params, err := a.client.GetParams(ctx)
	if err != nil {
		return nil, err
	}

	// ProofParams is an interface, we need to extract the concrete type
	// The actual implementation returns *prooftypes.Params
	if concreteParams, ok := params.(*prooftypes.Params); ok {
		return concreteParams, nil
	}

	// Fallback: try to get compute units per relay (ProofParams interface method)
	// and construct a new Params object
	computeUnitsPerRelay := params.GetProofRequestProbability()
	proofRequirementThreshold := params.GetProofRequirementThreshold()
	proofMissingPenalty := params.GetProofMissingPenalty()
	proofSubmissionFee := params.GetProofSubmissionFee()

	return &prooftypes.Params{
		ProofRequestProbability:   computeUnitsPerRelay,
		ProofRequirementThreshold: proofRequirementThreshold,
		ProofMissingPenalty:       proofMissingPenalty,
		ProofSubmissionFee:        proofSubmissionFee,
	}, nil
}

// NewProofQueryClientAdapter creates an adapter for client.ProofQueryClient.
func NewProofQueryClientAdapter(c client.ProofQueryClient) ProofQueryClient {
	return &proofQueryClientAdapter{client: c}
}

// sessionQueryClientAdapter adapts client.SessionQueryClient to SessionQueryClient.
// Note: client.SessionQueryClient likely already returns *sessiontypes.Params,
// but we create an adapter for consistency.
type sessionQueryClientAdapter struct {
	client client.SessionQueryClient
}

func (a *sessionQueryClientAdapter) GetParams(ctx context.Context) (*sessiontypes.Params, error) {
	return a.client.GetParams(ctx)
}

// NewSessionQueryClientAdapter creates an adapter for client.SessionQueryClient.
func NewSessionQueryClientAdapter(c client.SessionQueryClient) SessionQueryClient {
	return &sessionQueryClientAdapter{client: c}
}
