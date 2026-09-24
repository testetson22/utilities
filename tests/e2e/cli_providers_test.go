//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// PDescribe: the DCM CLI has not been updated since control-plane#51 removed the
// /providers API and replaced it with /agents.  Two blockers must be resolved:
//
//  1. CLI work (FLPATH-4895 — assigned, status: New):
//     `dcm sp provider list/get` hits GET /providers → 404.
//     CLI needs `dcm agent list/get` commands (or update `sp provider` to target /agents).
//
//  2. Test assertions (update below when CLI ships):
//     Change runDCM("sp", "provider", ...) → runDCM("agent", ...) (or equivalent),
//     and update the JSON key check to match whatever the CLI wraps agents under.
//
// The BeforeAll/AfterAll below have already been updated to use the /agents API.
// Re-enable by changing PDescribe → Describe once the CLI work is complete.
var _ = PDescribe("CLI: agent commands", Label("cli"), func() {
	Context("read operations", Ordered, func() {
		var agentID string
		agentName := fmt.Sprintf("e2e-cli-agent-%d", time.Now().UnixNano())

		// Register an agent via API so the CLI has something to read.
		// /agents has no DELETE endpoint — agents deregister via heartbeat timeout.
		BeforeAll(func() {
			payload := fmt.Sprintf(`{
				"name": %q,
				"environment": "e2e-test",
				"topic_name": "dcm.agent.%s",
				"service_types": ["vm"],
				"cost": "low"
			}`, agentName, agentName)

			resp, err := doRequest(http.MethodPost, "/agents", payload)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			var body map[string]interface{}
			decodeJSON(resp, &body)

			id, ok := body["agent_id"].(string)
			Expect(ok).To(BeTrue(), "agent_id should be a string")
			agentID = id
		})

		// TODO: update command name once CLI ships `dcm agent` subcommands.
		It("lists agents", func() {
			stdout, stderr, exitCode := runDCM("sp", "provider", "list")

			Expect(exitCode).To(Equal(0), "stderr: %s", stderr)
			Expect(stdout).To(ContainSubstring(agentID))
		})

		It("lists agents in JSON format", func() {
			stdout, stderr, exitCode := runDCM("sp", "provider", "list", "--output", "json")

			Expect(exitCode).To(Equal(0), "stderr: %s", stderr)

			var result map[string]interface{}
			Expect(json.Unmarshal([]byte(stdout), &result)).To(Succeed())
			// TODO: update key ("agents" or "results") to match CLI output once it ships.
			Expect(result).To(SatisfyAny(HaveKey("agents"), HaveKey("results")))
		})

		It("gets an agent by ID", func() {
			Expect(agentID).NotTo(BeEmpty(), "agent must be registered first")

			// TODO: update command name once CLI ships `dcm agent get`.
			stdout, stderr, exitCode := runDCM("sp", "provider", "get", agentID)

			Expect(exitCode).To(Equal(0), "stderr: %s", stderr)
			Expect(stdout).To(ContainSubstring(agentName))
		})
	})

	Context("error handling", func() {
		// TODO: update command name once CLI ships `dcm agent get`.
		It("returns exit code 1 for non-existent agent", func() {
			_, stderr, exitCode := runDCM("sp", "provider", "get", "does-not-exist")

			Expect(exitCode).To(Equal(1))
			Expect(stderr).NotTo(BeEmpty())
		})
	})
})
