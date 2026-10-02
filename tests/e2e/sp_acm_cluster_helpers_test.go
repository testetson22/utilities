//go:build e2e

package e2e_test

import (
	"io"
	"net/http"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
)

const defaultAcmClusterSPURL = "http://localhost:8083/api/v1alpha1"

var (
	acmClusterSPBaseURL string
	acmClusterSPReady   bool
)

func initAcmClusterSP() {
	acmClusterSPBaseURL = os.Getenv("DCM_ACM_CLUSTER_SP_URL")
	if acmClusterSPBaseURL == "" {
		acmClusterSPBaseURL = defaultAcmClusterSPURL
	}
	acmClusterSPBaseURL = strings.TrimRight(acmClusterSPBaseURL, "/")

	resp, err := unauthenticatedClient.Get(acmClusterSPBaseURL + "/clusters/health")
	if err != nil {
		GinkgoWriter.Printf("ACM Cluster SP not reachable at %s: %v — ACM SP tests will be skipped\n", acmClusterSPBaseURL, err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		GinkgoWriter.Printf("ACM Cluster SP health returned %d — ACM SP tests will be skipped\n", resp.StatusCode)
		return
	}
	acmClusterSPReady = true
	GinkgoWriter.Printf("ACM Cluster SP ready at %s\n", acmClusterSPBaseURL)
}

func requireAcmClusterSP() {
	if !acmClusterSPReady {
		Skip("ACM Cluster SP not available (deploy with --acm-cluster-service-provider and publish port 8083)")
	}
}

func doAcmClusterSPRequest(method, path string, body string) (*http.Response, error) {
	url := acmClusterSPBaseURL + path

	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return unauthenticatedClient.Do(req)
}

func deleteTestCluster(id string) {
	resp, err := doAcmClusterSPRequest(http.MethodDelete, "/clusters/"+id, "")
	if err != nil {
		GinkgoWriter.Printf("Warning: cleanup DELETE failed for cluster %s: %v\n", id, err)
		return
	}
	resp.Body.Close()
}
