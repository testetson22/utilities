package main

import (
	"context"

	publicv1 "github.com/dcm-project/osac-service-provider/internal/osacpb/osac/public/v1"
)

type clusterVersionsServer struct {
	publicv1.UnimplementedClusterVersionsServer
	version *publicv1.ClusterVersion
}

func newClusterVersionsServer() *clusterVersionsServer {
	enabled, defaultVersion := true, true
	return &clusterVersionsServer{version: &publicv1.ClusterVersion{
		Id:       "simulator-version-1.30",
		Metadata: &publicv1.Metadata{Name: "simulator-1-30"},
		Spec: &publicv1.ClusterVersionSpec{Version: "1.30", Enabled: &enabled, IsDefault: &defaultVersion, State: publicv1.ClusterVersionState_CLUSTER_VERSION_STATE_ACTIVE},
	}}
}

func (s *clusterVersionsServer) List(context.Context, *publicv1.ClusterVersionsListRequest) (*publicv1.ClusterVersionsListResponse, error) {
	return &publicv1.ClusterVersionsListResponse{Items: []*publicv1.ClusterVersion{s.version}, Size: 1, Total: 1}, nil
}

func (s *clusterVersionsServer) Get(_ context.Context, req *publicv1.ClusterVersionsGetRequest) (*publicv1.ClusterVersionsGetResponse, error) {
	return &publicv1.ClusterVersionsGetResponse{Object: s.version}, nil
}
