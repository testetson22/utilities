package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	clusterOrdersGVR    = schema.GroupVersionResource{Group: "osac.openshift.io", Version: "v1alpha1", Resource: "clusterorders"}
	computeInstancesGVR = schema.GroupVersionResource{Group: "osac.openshift.io", Version: "v1alpha1", Resource: "computeinstances"}
)

func main() {
	logger := slog.Default()
	config, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	}
	if err != nil {
		logger.Error("kubernetes config failed", "error", err)
		os.Exit(1)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		logger.Error("dynamic client failed", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	namespace := os.Getenv("SIMULATOR_OSAC_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	scenario := strings.ToLower(os.Getenv("SIMULATOR_SCENARIO"))
	if scenario == "" {
		scenario = "ready"
	}
	logger.Info("OSAC controller simulator started", "namespace", namespace, "scenario", scenario)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := reconcile(ctx, client, namespace, scenario); err != nil {
				logger.Warn("reconcile failed", "error", err)
			}
		}
	}
}

func reconcile(ctx context.Context, client dynamic.Interface, namespace, scenario string) error {
	orders, err := client.Resource(clusterOrdersGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list ClusterOrders: %w", err)
	}
	for i := range orders.Items {
		if err := patchStatus(ctx, client.Resource(clusterOrdersGVR).Namespace(namespace), &orders.Items[i], scenario); err != nil {
			return err
		}
	}
	instances, err := client.Resource(computeInstancesGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list ComputeInstances: %w", err)
	}
	for i := range instances.Items {
		if err := patchComputeStatus(ctx, client.Resource(computeInstancesGVR).Namespace(namespace), &instances.Items[i], scenario); err != nil {
			return err
		}
	}
	return nil
}

func patchStatus(ctx context.Context, resource dynamic.ResourceInterface, object *unstructured.Unstructured, scenario string) error {
	status := map[string]interface{}{}
	if scenario == "failed" {
		status["phase"] = "Failed"
		status["conditions"] = []interface{}{condition("Failed", "True", "SimulatorFailure", "Deterministic simulator failure")}
	} else if scenario == "active" {
		status["phase"] = "Ready"
		status["conditions"] = []interface{}{condition("Ready", "True", "SimulatorReady", "Deterministic simulator ready")}
	} else {
		status["phase"] = "Progressing"
		status["conditions"] = []interface{}{
			condition("NamespaceCreated", "True", "NamespaceCreated", "Simulator created the order namespace"),
			condition("Progressing", "True", "PreparingInfrastructure", "Simulator is preparing infrastructure"),
		}
	}
	status["provisioningJobs"] = []interface{}{map[string]interface{}{"jobID": "simulator-job-" + object.GetName(), "currentState": "successful"}}
	_, err := resource.UpdateStatus(ctx, &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": object.GetAPIVersion(), "kind": object.GetKind(), "metadata": map[string]interface{}{"name": object.GetName(), "namespace": object.GetNamespace(), "resourceVersion": object.GetResourceVersion()}, "status": status}}, metav1.UpdateOptions{})
	return err
}

func patchComputeStatus(ctx context.Context, resource dynamic.ResourceInterface, object *unstructured.Unstructured, scenario string) error {
	state := "RUNNING"
	if scenario == "failed" {
		state = "FAILED"
	}
	status := map[string]interface{}{"state": state}
	if state == "RUNNING" {
		status["internalIPAddress"] = "192.0.2.10"
		status["externalIPAddress"] = "198.51.100.10"
	}
	_, err := resource.UpdateStatus(ctx, &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": object.GetAPIVersion(), "kind": object.GetKind(), "metadata": map[string]interface{}{"name": object.GetName(), "namespace": object.GetNamespace(), "resourceVersion": object.GetResourceVersion()}, "status": status}}, metav1.UpdateOptions{})
	return err
}

func condition(conditionType, status, reason, message string) map[string]interface{} {
	return map[string]interface{}{"type": conditionType, "status": status, "reason": reason, "message": message}
}
