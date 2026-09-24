.PHONY: help e2e-up test-e2e test-smoke test-cli test-sp test-acm-sp test-kubevirt-sp test-osac-sp test-core test-rehydration test-rehydration-safe test-rehydration-cli e2e-down test-e2e-full download-cli cli-version lint

# Set JUNIT_REPORT to a filename to produce JUnit XML output.
# Example: make test-e2e JUNIT_REPORT=results.xml
JUNIT_REPORT ?=
CLI_VERSION ?= main
GINKGO_BASE = go run github.com/onsi/ginkgo/v2/ginkgo -r -v --tags=e2e
ifdef JUNIT_REPORT
GINKGO_BASE += --junit-report=$(JUNIT_REPORT)
endif

help: ## Show all available targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

e2e-up: ## Deploy the full DCM stack
	./scripts/deploy-dcm.sh

deploy-osac-backend: ## Deploy OSAC fulfillment-service backend on OCP (for OSAC SP E2E tests)
	./scripts/deploy-osac-backend.sh

teardown-osac-backend: ## Remove the OSAC backend from OCP
	./scripts/deploy-osac-backend.sh --tear-down

port-forward-osac: ## Mac/Darwin: tunnel OSAC backend via oc port-forward (8443=Keycloak, 19443=gRPC)
	@echo "==> Starting OSAC backend port-forwards (launchd-managed auto-restart)"
	@bash scripts/osac-port-forward.sh
	@sleep 3
	@lsof -i :8443 -sTCP:LISTEN | grep -q . && echo "    Keycloak  OK (8443)" || echo "    Keycloak  FAILED — check /tmp/pf-ffs-keycloak.log"
	@lsof -i :19443 -sTCP:LISTEN | grep -q . && echo "    gRPC      OK (19443)" || echo "    gRPC      FAILED — check /tmp/pf-fulfillment-grpc-server.log"
	@echo "==> Port-forwards running (launchd restart loop). Now deploy:"
	@echo "    ./scripts/deploy-dcm.sh --environment-agent --osac-service-provider"

stop-port-forward-osac: ## Stop OSAC backend port-forwards started by port-forward-osac
	@bash scripts/osac-port-forward.sh --stop

test-e2e: ## Run all E2E tests (stack must be running)
	cd tests/e2e && $(GINKGO_BASE) .

test-smoke: ## Run smoke tests only (health checks + CLI version)
	cd tests/e2e && $(GINKGO_BASE) --label-filter=smoke .

test-cli: ## Run CLI tests only (stack must be running)
	cd tests/e2e && $(GINKGO_BASE) --label-filter=cli .

test-sp: ## Run all service provider tests (SPs must be deployed with ports published)
	cd tests/e2e && $(GINKGO_BASE) --label-filter=sp .

test-acm-sp: ## Run ACM cluster SP tests only
	cd tests/e2e && $(GINKGO_BASE) --label-filter=acm-cluster .

test-kubevirt-sp: ## Run KubeVirt SP tests only (KubeVirt cluster required)
	cd tests/e2e && $(GINKGO_BASE) --label-filter=kubevirt .

test-osac-sp: ## Run OSAC SP tests only (--environment-agent --osac-service-provider required)
	cd tests/e2e && $(GINKGO_BASE) --label-filter=osac .

test-core: ## Run core platform tests (full control plane provisioning flow)
	cd tests/e2e && $(GINKGO_BASE) --label-filter=core .

test-rehydration: ## Run all rehydration tests (multi-provider stack with podman required)
	cd tests/e2e && $(GINKGO_BASE) --label-filter=rehydration .

test-rehydration-safe: ## Run non-disruptive rehydration tests only
	cd tests/e2e && $(GINKGO_BASE) --label-filter='rehydration && !disruptive' .

test-rehydration-cli: ## Run rehydration CLI tests only
	cd tests/e2e && $(GINKGO_BASE) --label-filter='rehydration && cli' .

e2e-down: ## Tear down the DCM stack
	./scripts/deploy-dcm.sh --tear-down

test-e2e-full: ## Deploy, test, and tear down (full lifecycle)
	./tests/run-e2e.sh $(if $(JUNIT_REPORT),--junit-report $(JUNIT_REPORT))

download-cli: ## Download DCM CLI from GitHub releases (CLI_VERSION=main)
	@command -v gh >/dev/null 2>&1 || { echo "ERROR: gh CLI required (https://cli.github.com)"; exit 1; }
	@mkdir -p bin
	@OS=$$(uname -s | tr '[:upper:]' '[:lower:]'); ARCH=$$(uname -m); case "$$ARCH" in x86_64) ARCH=amd64;; aarch64) ARCH=arm64;; esac; echo "==> Downloading DCM CLI ($(CLI_VERSION)) for $$OS/$$ARCH"; gh release download $(CLI_VERSION) --repo dcm-project/cli --pattern "cli_*_$${OS}_$${ARCH}.tar.gz" --dir bin --clobber; tar -xzf bin/cli_*_$${OS}_$${ARCH}.tar.gz -C bin dcm; rm -f bin/cli_*_$${OS}_$${ARCH}.tar.gz; chmod +x bin/dcm; echo "    Downloaded to bin/dcm"

CLI_VERSION_FILE ?= dcm-cli-version.json
cli-version: ## Write DCM CLI version info to JSON file
	@DCM_BIN="$${DCM_CLI_PATH:-}"; if [[ -z "$$DCM_BIN" ]]; then if command -v dcm &>/dev/null; then DCM_BIN="$$(command -v dcm)"; elif [[ -x bin/dcm ]]; then DCM_BIN="bin/dcm"; else echo "ERROR: dcm binary not found (set DCM_CLI_PATH or run make download-cli)"; exit 1; fi; fi; RAW="$$("$$DCM_BIN" version 2>&1)"; echo "$$RAW" | awk '/^dcm version/{v=$$0; sub(/^dcm version /,"",v)} /commit:/{sub(/^ *commit: */,""); c=$$0} /built:/{sub(/^ *built: */,""); b=$$0} /go:/{sub(/^ *go: */,""); g=$$0} END{printf "{\"version\":\"%s\",\"commit\":\"%s\",\"built\":\"%s\",\"go\":\"%s\"}\n",v,c,b,g}' | jq . > $(CLI_VERSION_FILE); echo "==> Wrote $(CLI_VERSION_FILE)"; cat $(CLI_VERSION_FILE)

SHELL_SCRIPTS = scripts/*.sh scripts/kind/*.sh scripts/compose/*.sh scripts/kubevirt/*.sh tests/*.sh
.PHONY: deploy-osac-backend teardown-osac-backend port-forward-osac stop-port-forward-osac
SHELLCHECK_BIN := $(shell command -v shellcheck 2>/dev/null)
ifeq ($(SHELLCHECK_BIN),)
CONTAINER_ENGINE ?= $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null)
SHELLCHECK = $(CONTAINER_ENGINE) run --rm -v "$(CURDIR):/mnt" -w /mnt docker.io/koalaman/shellcheck:stable -x
else
SHELLCHECK = $(SHELLCHECK_BIN) -x
endif

lint: ## Lint all shell scripts with ShellCheck (uses a container if shellcheck is not installed)
	@command -v shellcheck >/dev/null 2>&1 || { \
		command -v $(firstword $(SHELLCHECK)) >/dev/null 2>&1 || { \
			echo "ERROR: shellcheck not found and no container engine (podman/docker) available"; \
			echo "Install shellcheck (e.g. apt install shellcheck) or podman/docker"; \
			exit 1; \
		}; \
		echo "==> shellcheck not found locally; using $(firstword $(SHELLCHECK))"; \
	}
	$(SHELLCHECK) $(SHELL_SCRIPTS)