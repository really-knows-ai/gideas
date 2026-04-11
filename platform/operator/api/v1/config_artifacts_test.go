package v1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSampleFoundryFlowUsesImportTypes(t *testing.T) {
	t.Parallel()

	content := mustReadFile(t, filepath.Join("..", "..", "config", "samples", "flow_v1_foundryflow.yaml"))
	assertContains(t, content, "crossFlow:")
	assertContains(t, content, "importTypes:")
	assertContains(t, content, "law-petition:")
	assertNotContains(t, content, "importNode:")
}

func TestSampleTreatyUsesAllowedImportTypes(t *testing.T) {
	t.Parallel()

	content := mustReadFile(t, filepath.Join("..", "..", "config", "samples", "flow_v1_treaty.yaml"))
	assertContains(t, content, "remoteName:")
	assertContains(t, content, "direction: import")
	assertContains(t, content, "allowedImportTypes:")
	assertNotContains(t, content, "TODO(user)")
}

func TestGeneratedFoundryFlowCRDUsesEmbassyFields(t *testing.T) {
	t.Parallel()

	content := mustReadFile(t, filepath.Join("..", "..", "config", "crd", "bases", "flow.gideas.io_foundryflows.yaml"))
	assertContains(t, content, "federationCA:")
	assertContains(t, content, "importTypes:")
	assertNotContains(t, content, "stateRootCA:")
	assertNotContains(t, content, "importNode:")
}

func TestGeneratedTreatyCRDUsesAllowedImportTypes(t *testing.T) {
	t.Parallel()

	content := mustReadFile(t, filepath.Join("..", "..", "config", "crd", "bases", "flow.gideas.io_treaties.yaml"))
	assertContains(t, content, "allowedImportTypes:")
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertContains(t *testing.T, content, want string) {
	t.Helper()
	if !strings.Contains(content, want) {
		t.Fatalf("expected content to contain %q", want)
	}
}

func assertNotContains(t *testing.T, content, want string) {
	t.Helper()
	if strings.Contains(content, want) {
		t.Fatalf("expected content not to contain %q", want)
	}
}
