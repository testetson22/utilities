package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	publicv1 "github.com/dcm-project/osac-service-provider/internal/osacpb/osac/public/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type scenarioState struct {
	name        string
	deleteAfter int
	mu          sync.Mutex
	deletes     map[string]int
}

func newScenarioState() *scenarioState {
	deleteAfter, _ := strconv.Atoi(os.Getenv("SIMULATOR_DELETE_AFTER"))
	if deleteAfter < 1 {
		deleteAfter = 2
	}
	return &scenarioState{
		name: strings.ToLower(os.Getenv("SIMULATOR_SCENARIO")), deleteAfter: deleteAfter,
		deletes: make(map[string]int),
	}
}

func (s *scenarioState) unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		id := requestID(req)
		if s.name == "backend-unavailable" && isResourceGet(info.FullMethod) {
			return nil, status.Error(codes.Unavailable, "simulated fulfillment-service unavailable")
		}
		if s.name == "backend-timeout" && isResourceGet(info.FullMethod) {
			return nil, status.Error(codes.DeadlineExceeded, "simulated fulfillment-service deadline")
		}
		if s.name == "delete-delayed" && strings.HasSuffix(info.FullMethod, "/Delete") && id != "" {
			s.mu.Lock()
			s.deletes[id] = 0
			s.mu.Unlock()
			switch req.(type) {
			case *publicv1.ClustersDeleteRequest:
				return &publicv1.ClustersDeleteResponse{}, nil
			case *publicv1.ComputeInstancesDeleteRequest:
				return &publicv1.ComputeInstancesDeleteResponse{}, nil
			}
		}
		if s.name == "delete-delayed" && strings.HasSuffix(info.FullMethod, "/Get") && id != "" {
			s.mu.Lock()
			count := s.deletes[id]
			if count > 0 {
				s.deletes[id] = count + 1
			}
			if count >= s.deleteAfter {
				delete(s.deletes, id)
				s.mu.Unlock()
				return nil, status.Error(codes.NotFound, "simulated delayed deletion complete")
			}
			if count == 0 {
				s.deletes[id] = 1
			}
			s.mu.Unlock()
		}
		if s.name == "delete-delayed" && strings.HasSuffix(info.FullMethod, "/List") {
			s.mu.Lock()
			for key, count := range s.deletes {
				s.deletes[key] = count + 1
			}
			s.mu.Unlock()
		}

		response, err := handler(ctx, req)
		if err != nil {
			return response, err
		}
		s.apply(response)
		if s.name == "delete-delayed" && strings.HasSuffix(info.FullMethod, "/List") {
			s.filterDeleted(response)
		}
		return response, nil
	}
}

func (s *scenarioState) filterDeleted(response interface{}) {
	s.mu.Lock()
	deleting := make(map[string]bool, len(s.deletes))
	for id, count := range s.deletes {
		if count >= s.deleteAfter {
			deleting[id] = true
		}
	}
	s.mu.Unlock()
	if len(deleting) == 0 {
		return
	}
	switch list := response.(type) {
	case *publicv1.ClustersListResponse:
		filtered := list.Items[:0]
		for _, item := range list.Items {
			if item == nil || !deleting[item.Id] {
				filtered = append(filtered, item)
			}
		}
		list.Items = filtered
	case *publicv1.ComputeInstancesListResponse:
		filtered := list.Items[:0]
		for _, item := range list.Items {
			if item == nil || !deleting[item.Id] {
				filtered = append(filtered, item)
			}
		}
		list.Items = filtered
	}
}

func isResourceGet(method string) bool {
	return (strings.Contains(method, ".Clusters/Get") || strings.Contains(method, ".ComputeInstances/Get")) && strings.HasSuffix(method, "/Get")
}

func requestID(req interface{}) string {
	switch r := req.(type) {
	case *publicv1.ClustersGetRequest:
		return r.GetId()
	case *publicv1.ClustersDeleteRequest:
		return r.GetId()
	case *publicv1.ComputeInstancesGetRequest:
		return r.GetId()
	case *publicv1.ComputeInstancesDeleteRequest:
		return r.GetId()
	default:
		return ""
	}
}

func (s *scenarioState) apply(response interface{}) {
	if s.name != "failed" {
		if s.name == "vm-running" {
			s.applyVM(response, publicv1.ComputeInstanceState_COMPUTE_INSTANCE_STATE_RUNNING)
		}
		return
	}
	switch r := response.(type) {
	case *publicv1.ClustersGetResponse:
		if r.Object != nil {
			r.Object.Status.State = publicv1.ClusterState_CLUSTER_STATE_FAILED
		}
	case *publicv1.ClustersListResponse:
		for _, item := range r.Items {
			if item != nil && item.Status != nil {
				item.Status.State = publicv1.ClusterState_CLUSTER_STATE_FAILED
			}
		}
	case *publicv1.ComputeInstancesGetResponse:
		s.applyVM(r, publicv1.ComputeInstanceState_COMPUTE_INSTANCE_STATE_FAILED)
	case *publicv1.ComputeInstancesListResponse:
		for _, item := range r.Items {
			if item != nil && item.Status != nil {
				item.Status.State = publicv1.ComputeInstanceState_COMPUTE_INSTANCE_STATE_FAILED
			}
		}
	}
}

func (s *scenarioState) applyVM(response interface{}, state publicv1.ComputeInstanceState) {
	set := func(instance *publicv1.ComputeInstance) {
		if instance == nil || instance.Status == nil {
			return
		}
		instance.Status.State = state
		if state == publicv1.ComputeInstanceState_COMPUTE_INSTANCE_STATE_RUNNING {
			instance.Status.InternalIpAddress = "192.0.2.10"
			instance.Status.ExternalIpAddress = "198.51.100.10"
		}
	}
	switch r := response.(type) {
	case *publicv1.ComputeInstancesGetResponse:
		set(r.Object)
	case *publicv1.ComputeInstancesListResponse:
		for _, item := range r.Items {
			set(item)
		}
	}
}
