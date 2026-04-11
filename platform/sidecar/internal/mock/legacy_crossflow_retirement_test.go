package mock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyCrossFlowMethodsRemovedFromSidecarSources(t *testing.T) {
	t.Parallel()

	handlers := readRepoFile(t, "handlers.go")
	assertSourceNotContains(t, handlers, "ExportWorkitem(")
	assertSourceNotContains(t, handlers, "ImportWorkitem(")

	proxy := readRepoFile(t, filepath.Join("..", "proxy", "operator.go"))
	assertSourceNotContains(t, proxy, "ExportWorkitem(")
	assertSourceNotContains(t, proxy, "ImportWorkitem(")
	assertSourceNotContains(t, proxy, ".ExportWorkitem(")
	assertSourceNotContains(t, proxy, ".ImportWorkitem(")
}

func readRepoFile(t *testing.T, relativePath string) string {
	t.Helper()

	data, err := os.ReadFile(relativePath)
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(data)
}

func assertSourceNotContains(t *testing.T, content, want string) {
	t.Helper()
	if strings.Contains(content, want) {
		t.Fatalf("expected content not to contain %q", want)
	}
}
