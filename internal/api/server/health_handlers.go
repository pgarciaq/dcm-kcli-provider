package server

import (
	"context"
	"time"
)

func (s *StrictServerImpl) doHealthCheck(ctx context.Context) (HealthStatus, *string, *float32, *string) {
	uptime := float32(time.Since(s.startedAt).Seconds())
	ver := s.version

	healthy, err := s.kweb.CheckHealth(ctx)
	if err != nil || !healthy {
		msg := "kweb unreachable"
		if err != nil {
			msg = err.Error()
		}
		return Unhealthy, &ver, &uptime, &msg
	}
	return Healthy, &ver, &uptime, nil
}

func (s *StrictServerImpl) GetHealth(ctx context.Context, _ GetHealthRequestObject) (GetHealthResponseObject, error) {
	status, ver, uptime, msg := s.doHealthCheck(ctx)
	if status == Unhealthy {
		return GetHealth503JSONResponse{
			Status: &status, Version: ver, Uptime: uptime, Message: msg,
		}, nil
	}
	return GetHealth200JSONResponse{
		Status: &status, Version: ver, Uptime: uptime,
	}, nil
}

func (s *StrictServerImpl) GetVMHealth(ctx context.Context, _ GetVMHealthRequestObject) (GetVMHealthResponseObject, error) {
	status, ver, uptime, msg := s.doHealthCheck(ctx)
	if status == Unhealthy {
		return GetVMHealth503JSONResponse{
			Status: &status, Version: ver, Uptime: uptime, Message: msg,
		}, nil
	}
	return GetVMHealth200JSONResponse{
		Status: &status, Version: ver, Uptime: uptime,
	}, nil
}

func (s *StrictServerImpl) GetClusterHealth(ctx context.Context, _ GetClusterHealthRequestObject) (GetClusterHealthResponseObject, error) {
	status, ver, uptime, msg := s.doHealthCheck(ctx)
	if status == Unhealthy {
		return GetClusterHealth503JSONResponse{
			Status: &status, Version: ver, Uptime: uptime, Message: msg,
		}, nil
	}
	return GetClusterHealth200JSONResponse{
		Status: &status, Version: ver, Uptime: uptime,
	}, nil
}
