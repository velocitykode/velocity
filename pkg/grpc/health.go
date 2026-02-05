package grpc

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// HealthChecker is a function that performs a health check.
// Returns nil if healthy, or an error describing the issue.
type HealthChecker func(ctx context.Context) error

// HealthService implements the gRPC Health checking protocol.
// See: https://github.com/grpc/grpc/blob/master/doc/health-checking.md
type HealthService struct {
	grpc_health_v1.UnimplementedHealthServer
	mu       sync.RWMutex
	status   map[string]grpc_health_v1.HealthCheckResponse_ServingStatus
	checkers map[string]HealthChecker
}

// NewHealthService creates a new health service
func NewHealthService() *HealthService {
	return &HealthService{
		status: map[string]grpc_health_v1.HealthCheckResponse_ServingStatus{
			"": grpc_health_v1.HealthCheckResponse_SERVING,
		},
		checkers: make(map[string]HealthChecker),
	}
}

// Check implements the Health.Check RPC
func (s *HealthService) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	s.mu.RLock()
	checker, hasChecker := s.checkers[req.Service]
	currentStatus, hasStatus := s.status[req.Service]
	s.mu.RUnlock()

	// If there's a dynamic checker, use it
	if hasChecker {
		if err := checker(ctx); err != nil {
			return &grpc_health_v1.HealthCheckResponse{
				Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
			}, nil
		}
		return &grpc_health_v1.HealthCheckResponse{
			Status: grpc_health_v1.HealthCheckResponse_SERVING,
		}, nil
	}

	// Otherwise use static status
	if !hasStatus {
		return &grpc_health_v1.HealthCheckResponse{
			Status: grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN,
		}, nil
	}

	return &grpc_health_v1.HealthCheckResponse{
		Status: currentStatus,
	}, nil
}

// Watch implements the Health.Watch RPC for streaming health checks.
// This is a simple implementation that sends the current status and then blocks.
func (s *HealthService) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	s.mu.RLock()
	currentStatus, ok := s.status[req.Service]
	s.mu.RUnlock()

	if !ok {
		currentStatus = grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN
	}

	// Send initial status
	if err := stream.Send(&grpc_health_v1.HealthCheckResponse{
		Status: currentStatus,
	}); err != nil {
		return err
	}

	// Block until context is cancelled
	<-stream.Context().Done()
	return stream.Context().Err()
}

// SetServingStatus sets the serving status for a service
func (s *HealthService) SetServingStatus(service string, status grpc_health_v1.HealthCheckResponse_ServingStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[service] = status
}

// SetServing marks a service as serving
func (s *HealthService) SetServing(service string) {
	s.SetServingStatus(service, grpc_health_v1.HealthCheckResponse_SERVING)
}

// SetNotServing marks a service as not serving
func (s *HealthService) SetNotServing(service string) {
	s.SetServingStatus(service, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
}

// RegisterChecker registers a dynamic health checker for a service.
// The checker is called on each health check request.
func (s *HealthService) RegisterChecker(service string, checker HealthChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkers[service] = checker
}

// RemoveChecker removes a dynamic health checker for a service
func (s *HealthService) RemoveChecker(service string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checkers, service)
}

// GetStatus returns the current status for a service
func (s *HealthService) GetStatus(service string) (grpc_health_v1.HealthCheckResponse_ServingStatus, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.status[service]
	return status, ok
}

// Register registers the health service with a gRPC server
func (s *HealthService) Register(server *grpc.Server) {
	grpc_health_v1.RegisterHealthServer(server, s)
}

// RegistrationFunc returns a registration function for use with Server.RegisterService
func (s *HealthService) RegistrationFunc() RegistrationFunc {
	return func(srv interface{}) {
		grpc_health_v1.RegisterHealthServer(srv.(*grpc.Server), s)
	}
}
