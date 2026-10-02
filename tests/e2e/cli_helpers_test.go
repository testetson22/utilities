//go:build e2e

package e2e_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	dcmBinaryPath string
	cliAvailable  bool
)

// initCLI resolves the DCM CLI binary path. Called from BeforeSuite.
// CLI tests are skipped (not failed) if no binary is found.
func initCLI() {
	if path := os.Getenv("DCM_CLI_PATH"); path != "" {
		dcmBinaryPath = path
	} else if path, err := exec.LookPath("dcm"); err == nil {
		dcmBinaryPath = path
	}

	if dcmBinaryPath != "" {
		if _, err := os.Stat(dcmBinaryPath); err == nil {
			cliAvailable = true
			GinkgoWriter.Printf("Using DCM CLI: %s\n", dcmBinaryPath)
			return
		}
	}

	GinkgoWriter.Println("DCM CLI binary not found — CLI tests will be skipped")
}

// requireCLI skips the current test if the CLI binary is not available.
func requireCLI() {
	if !cliAvailable {
		Skip("DCM CLI binary not available (set DCM_CLI_PATH or install dcm)")
	}
}

// cliControlPlaneURL returns the control plane base URL without the /api/v1alpha1 suffix.
func cliControlPlaneURL() string {
	return strings.TrimSuffix(gatewayBaseURL, "/api/v1alpha1")
}

// runDCM executes the DCM CLI binary with the given arguments and returns
// stdout, stderr, and the exit code. It automatically injects
// --control-plane-url and --config flags for test isolation.
func runDCM(args ...string) (stdout string, stderr string, exitCode int) {
	requireCLI()

	// Use a nonexistent config path to isolate from ~/.dcm/config.yaml.
	// /dev/null won't work — Viper rejects it as unsupported config type.
	configPath := filepath.Join(os.TempDir(), "dcm-e2e-nonexistent.yaml")
	fullArgs := []string{
		"--control-plane-url", cliControlPlaneURL(),
		"--config", configPath,
	}
	fullArgs = append(fullArgs, args...)

	cmd := exec.Command(dcmBinaryPath, fullArgs...)
	cmd.Env = os.Environ()
	if authEnabled {
		token, err := authTokenForCLI()
		Expect(err).NotTo(HaveOccurred())
		cmd.Env = append(cmd.Env, "DCM_TOKEN="+token)
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			// Binary not found or OS-level error — fail the test.
			Expect(err).NotTo(HaveOccurred(), "failed to execute dcm binary")
		}
	}

	return stdout, stderr, exitCode
}

// writeTempFile creates a temporary file with the given content and suffix,
// returning its path. The file is automatically cleaned up by Ginkgo.
func writeTempFile(content, suffix string) string {
	f, err := os.CreateTemp(GinkgoT().TempDir(), "e2e-*"+suffix)
	Expect(err).NotTo(HaveOccurred())
	_, err = f.WriteString(content)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.Close()).To(Succeed())
	return f.Name()
}
