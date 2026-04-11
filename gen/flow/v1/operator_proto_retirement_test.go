package flowv1_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorProtoNoLongerDefinesCrossFlowRPCs(t *testing.T) {
	t.Parallel()

	content := mustReadRepoFile(t, filepath.Join("..", "..", "..", "proto", "flow", "v1", "operator.proto"))
	assertNotContains(t, content, "rpc ExportWorkitem")
	assertNotContains(t, content, "rpc ImportWorkitem")
	assertNotContains(t, content, "message ExportWorkitemRequest")
	assertNotContains(t, content, "message ImportWorkitemRequest")
}

func mustReadRepoFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertNotContains(t *testing.T, content, want string) {
	t.Helper()
	if strings.Contains(content, want) {
		t.Fatalf("expected content not to contain %q", want)
	}
}
