package v1

import (
	"path/filepath"
	"testing"
)

func TestGeneratedFoundryGraphCRD_HasMaxLength(t *testing.T) {
	t.Parallel()

	content := mustReadFile(t, filepath.Join("..", "..", "config", "crd", "bases", "flow.foundry.io_foundrygraphs.yaml"))
	assertContains(t, content, "maxLength: 255")
}

func TestGeneratedFoundryGraphCRD_HasCypherIdentifierPattern(t *testing.T) {
	t.Parallel()

	content := mustReadFile(t, filepath.Join("..", "..", "config", "crd", "bases", "flow.foundry.io_foundrygraphs.yaml"))
	assertContains(t, content, "pattern: ^[a-zA-Z_][a-zA-Z0-9_]*$")
}

func TestGeneratedFoundryGraphCRD_HasStringPropertyEnum(t *testing.T) {
	t.Parallel()

	content := mustReadFile(t, filepath.Join("..", "..", "config", "crd", "bases", "flow.foundry.io_foundrygraphs.yaml"))
	assertContains(t, content, "enum:")
	assertContains(t, content, "- string")
}
