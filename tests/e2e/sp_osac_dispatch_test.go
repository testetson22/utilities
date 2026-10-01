//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This suite deliberately implements only the checkpoints that can be proven
// without assuming Agent, BMH, or AAP behavior. TBP-040 and TBP-050 remain
// discovery outputs until a stable linkage is observed in this environment.
var _ = Describe("OSAC SP — Tier B+ dispatch discovery", Label("sp", "osac", "tier-b-dispatch", "cluster"), Ordered, func() {
	var (
		clusterID     string
		clusterName   string
		clusterOrder  map[string]interface{}
		beforeOrders  map[string]map[string]interface{}
		nc            *nats.Conn
		sub           *nats.Subscription
		matchingEvent *osacCloudEvent
		created       bool
	)

	BeforeAll(func() {
		requireOsacSP()
		if osacTemplateID() == "" {
			Skip("OSAC_E2E_CLUSTER_TEMPLATE_ID not set — Tier B+ discovery requires a real backend")
		}
		requireOSACKubectl()

		beforeOrders = osacClusterOrders()
		clusterName = uniqueName("e2e-tierb-dispatch")

		natsAddr := os.Getenv("DCM_NATS_URL")
		if natsAddr == "" {
			natsAddr = defaultNATSURL
		}
		var err error
		nc, err = nats.Connect(natsAddr, nats.Timeout(5*time.Second), nats.Name("dcm-tier-b-dispatch"))
		Expect(err).NotTo(HaveOccurred(), "connect to NATS at %s", natsAddr)
		sub, err = nc.SubscribeSync(osacClusterNATSSubject)
		Expect(err).NotTo(HaveOccurred())
		Expect(nc.Flush()).To(Succeed())

		resp, err := doOsacClusterRequest(http.MethodPost,
			fmt.Sprintf("/clusters?id=%s", clusterName), osacClusterPayload(clusterName))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusCreated), "SP create must be accepted")

		var createResp osacCreateResponse
		decodeJSON(resp, &createResp)
		clusterID = osacIDFromCreateResponse(createResp)
		Expect(clusterID).NotTo(BeEmpty(), "SP create response must contain an ID")
		created = true
	})

	AfterAll(func() {
		if sub != nil {
			_ = sub.Unsubscribe()
		}
		if nc != nil {
			nc.Close()
		}
		if created {
			deleteTestOsacCluster(clusterID)
		}
	})

	It("TBP-010 correlates SP create identity, GET/list identity, and NATS event", func() {
		resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var got osacCluster
		decodeJSON(resp, &got)
		Expect(got.ID).To(Equal(clusterID), "GET must return the exact created ID")

		resp, err = doOsacClusterRequest(http.MethodGet, "/clusters", "")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var list osacClusterListResponse
		decodeJSON(resp, &list)
		Expect(list.Results).To(ContainElement(HaveField("ID", clusterID)),
			"list must contain the exact created ID")

		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			msg, msgErr := sub.NextMsg(5 * time.Second)
			if msgErr != nil {
				continue
			}
			var event osacCloudEvent
			if json.Unmarshal(msg.Data, &event) == nil && event.Data.ID == clusterID {
				matchingEvent = &event
				break
			}
		}
		Expect(matchingEvent).NotTo(BeNil(), "no dcm.cluster event for exact SP ID %s", clusterID)
		Expect(matchingEvent.Data.ID).To(Equal(clusterID))
		Expect(osacClusterStatusValid(matchingEvent.Data.Status)).To(BeTrue(),
			"NATS status %q must use the OSAC cluster vocabulary", matchingEvent.Data.Status)
	})

	It("TBP-020 discovers a new ClusterOrder with verified linkage to the SP ID", func() {
		Eventually(func() error {
			orders := osacClusterOrders()
			for key, order := range orders {
				if _, existed := beforeOrders[key]; existed {
					continue
				}
				if osacObjectReferencesID(order, clusterID) {
					clusterOrder = order
					return nil
				}
			}
			return fmt.Errorf("no newly created ClusterOrder references SP ID %s; observed %s", clusterID, osacObjectNames(orders))
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		Expect(clusterOrder).NotTo(BeNil())
		Expect(osacObjectReferencesID(clusterOrder, clusterID)).To(BeTrue(),
			"ClusterOrder must carry a stable label, annotation, owner reference, or spec reference to the exact SP ID")
		templateID, found := nestedString(clusterOrder, "spec", "templateID")
		Expect(found).To(BeTrue(), "linked ClusterOrder must expose the translated templateID")
		Expect(templateID).To(Equal(osacTemplateID()),
			"ClusterOrder templateID must preserve the exact SP request template")
	})

	It("TBP-030 reports a linked ClusterOrder condition and corresponding SP state", func() {
		Expect(clusterOrder).NotTo(BeNil(), "TBP-020 must establish the linked ClusterOrder first")
		name := osacObjectName(clusterOrder)
		namespace := osacObjectNamespace(clusterOrder)
		var conditions []interface{}
		Eventually(func() error {
			current, err := osacGetClusterOrder(name, namespace)
			if err != nil {
				return err
			}
			clusterOrder = current
			var found bool
			conditions, found = nestedSlice(current, "status", "conditions")
			if !found || len(conditions) == 0 {
				return fmt.Errorf("ClusterOrder %s has no status.conditions yet; object=%s", name, compactJSON(current))
			}
			return nil
		}).WithTimeout(3 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		Expect(conditions).NotTo(BeEmpty())
		for _, raw := range conditions {
			condition, ok := raw.(map[string]interface{})
			Expect(ok).To(BeTrue(), "ClusterOrder condition must be an object")
			conditionType, _ := condition["type"].(string)
			conditionStatus, _ := condition["status"].(string)
			Expect(conditionType).NotTo(BeEmpty(), "condition type must be present")
			Expect(conditionStatus).To(BeElementOf("True", "False", "Unknown"),
				"condition %s must have a Kubernetes condition status", conditionType)
			GinkgoWriter.Printf("ClusterOrder %s condition: type=%s status=%s reason=%v message=%v\n",
				name, conditionType, conditionStatus, condition["reason"], condition["message"])
		}
		if os.Getenv("OSAC_AAP_MODE") == "real" {
			jobs, found := nestedSlice(clusterOrder, "status", "provisioningJobs")
			Expect(found).To(BeTrue(), "real-AAP ClusterOrder must record provisioning job history")
			Expect(jobs).NotTo(BeEmpty(), "real-AAP ClusterOrder must record the linked job ID")
			for _, raw := range jobs {
				job, ok := raw.(map[string]interface{})
				Expect(ok).To(BeTrue(), "provisioning job history entries must be objects")
				jobID, hasID := job["id"].(string)
				if !hasID {
					jobID, hasID = job["jobID"].(string)
				}
				Expect(hasID && jobID != "").To(BeTrue(), "provisioning job history must contain an AAP job ID")
				state, hasState := job["state"].(string)
				if !hasState {
					state, hasState = job["currentState"].(string)
				}
				Expect(hasState && state != "").To(BeTrue(), "provisioning job history must contain state")
			}
		}

		resp, err := doOsacClusterRequest(http.MethodGet, "/clusters/"+clusterID, "")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK),
			"SP GET must remain reachable while reporting operator state")
		var spCluster osacCluster
		decodeJSON(resp, &spCluster)
		Expect(spCluster.ID).To(Equal(clusterID))
		Expect(osacClusterStatusValid(spCluster.Status)).To(BeTrue(),
			"SP state %q must be in the documented vocabulary", spCluster.Status)
	})
})

func osacKubernetesNamespace() string {
	if ns := os.Getenv("OSAC_E2E_KUBERNETES_NAMESPACE"); ns != "" {
		return ns
	}
	return "osac-test-backend"
}

func requireOSACKubectl() {
	if kubectlBin == "" || !kubectlAvailable {
		Skip("oc/kubectl is not available or the configured cluster is unreachable")
	}
}

func osacClusterOrders() map[string]map[string]interface{} {
	out, err := runOSACKubectlAll("get", "clusterorders", "-A", "-o", "json")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "list ClusterOrders across all namespaces")
	var list struct {
		Items []map[string]interface{} `json:"items"`
	}
	ExpectWithOffset(1, json.Unmarshal([]byte(out), &list)).To(Succeed())
	orders := make(map[string]map[string]interface{}, len(list.Items))
	for _, item := range list.Items {
		orders[osacObjectKey(item)] = item
	}
	return orders
}

func osacGetClusterOrder(name, namespace string) (map[string]interface{}, error) {
	if namespace == "" {
		return nil, fmt.Errorf("ClusterOrder %s has no metadata.namespace", name)
	}
	out, err := runOSACKubectlAll("get", "clusterorders", name, "-n", namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var item map[string]interface{}
	if err := json.Unmarshal([]byte(out), &item); err != nil {
		return nil, err
	}
	return item, nil
}

func runOSACKubectl(args ...string) (string, error) {
	full := append([]string{"-n", osacKubernetesNamespace()}, args...)
	return runOSACKubectlArgs(full)
}

func runOSACKubectlAll(args ...string) (string, error) {
	return runOSACKubectlArgs(args)
}

func runOSACKubectlArgs(args []string) (string, error) {
	cmd := exec.Command(kubectlBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w", kubectlBin, args, err)
	}
	return string(out), nil
}

func osacObjectName(object map[string]interface{}) string {
	metadata, _ := object["metadata"].(map[string]interface{})
	name, _ := metadata["name"].(string)
	return name
}

func osacObjectNamespace(object map[string]interface{}) string {
	metadata, _ := object["metadata"].(map[string]interface{})
	namespace, _ := metadata["namespace"].(string)
	return namespace
}

func osacObjectKey(object map[string]interface{}) string {
	return osacObjectNamespace(object) + "/" + osacObjectName(object)
}

func osacObjectReferencesID(object map[string]interface{}, id string) bool {
	metadata, _ := object["metadata"].(map[string]interface{})
	if name, _ := metadata["name"].(string); name == id {
		return true
	}
	for _, field := range []string{"labels", "annotations"} {
		values, _ := metadata[field].(map[string]interface{})
		for _, value := range values {
			if value == id {
				return true
			}
		}
	}
	if owners, ok := metadata["ownerReferences"].([]interface{}); ok && containsStringValue(owners, id) {
		return true
	}
	return containsStringValue(object["spec"], id)
}

func containsStringValue(value interface{}, want string) bool {
	switch typed := value.(type) {
	case string:
		return typed == want
	case map[string]interface{}:
		for _, child := range typed {
			if containsStringValue(child, want) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if containsStringValue(child, want) {
				return true
			}
		}
	}
	return false
}

func osacObjectNames(objects map[string]map[string]interface{}) string {
	names := make([]string, 0, len(objects))
	for name := range objects {
		names = append(names, name)
	}
	return strings.Join(names, ",")
}

func nestedSlice(object map[string]interface{}, fields ...string) ([]interface{}, bool) {
	var current interface{} = object
	for _, field := range fields {
		values, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = values[field]
		if !ok {
			return nil, false
		}
	}
	result, ok := current.([]interface{})
	return result, ok
}

func nestedString(object map[string]interface{}, fields ...string) (string, bool) {
	var current interface{} = object
	for _, field := range fields {
		values, ok := current.(map[string]interface{})
		if !ok {
			return "", false
		}
		current, ok = values[field]
		if !ok {
			return "", false
		}
	}
	result, ok := current.(string)
	return result, ok
}

func compactJSON(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}
