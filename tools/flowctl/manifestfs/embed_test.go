package manifestfs

// The embedded manifests are regenerated snapshots of the operator's config/
// sources (`make flowctl-manifests`, mirrored by the //go:generate lines in
// embed.go). These guards pin every embedded copy against its source read from
// disk — never against the embedded copy itself — so a renamed, removed, or
// drifted source file surfaces here instead of silently shipping in
// `flowctl init`.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// operatorSrc resolves a path under platform/operator/config relative to this
// package (tools/flowctl/manifestfs).
func operatorSrc(t *testing.T, elem ...string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "platform", "operator", filepath.Join(elem...))
}

// TestEmbeddedCRDsMatchSourceBases pins the embedded crd/ set against
// config/crd/bases/: every source CRD must be embedded under the same
// filename with identical bytes, and no embedded file may lack a source
// counterpart (a missing CRD like foundrygraphs, a stale flow.gideas.io_*
// rename, or a removed CRD all fail here).
func TestEmbeddedCRDsMatchSourceBases(t *testing.T) {
	srcDir := operatorSrc(t, "config", "crd", "bases")
	srcEntries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read source CRD bases: %v", err)
	}
	embeddedEntries, err := Manifests.ReadDir("crd")
	if err != nil {
		t.Fatalf("read embedded crd/: %v", err)
	}

	srcNames := make(map[string]bool, len(srcEntries))
	for _, e := range srcEntries {
		if !e.IsDir() {
			srcNames[e.Name()] = true
		}
	}
	embeddedNames := make(map[string]bool, len(embeddedEntries))
	for _, e := range embeddedEntries {
		if !e.IsDir() {
			embeddedNames[e.Name()] = true
		}
	}

	for name := range srcNames {
		if !embeddedNames[name] {
			t.Errorf("embedded crd/ is missing %s (source bases has it); run `make flowctl-manifests`", name)
			continue
		}
		src, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			t.Fatalf("read source CRD %s: %v", name, err)
		}
		embedded, err := Manifests.ReadFile("crd/" + name)
		if err != nil {
			t.Fatalf("read embedded CRD %s: %v", name, err)
		}
		if !bytes.Equal(src, embedded) {
			t.Errorf("embedded crd/%s differs from source bases/%s; run `make flowctl-manifests`", name, name)
		}
	}
	for name := range embeddedNames {
		if !srcNames[name] {
			t.Errorf("embedded crd/%s has no counterpart in source bases (stale copy); run `make flowctl-manifests`", name)
		}
	}
}

// TestEmbeddedOperatorManifestsMatchSources pins the embedded operator/ files
// against their sources, applying the same transformations as the
// flowctl-manifests target (namespace rewrite, single-document Deployment).
func TestEmbeddedOperatorManifestsMatchSources(t *testing.T) {
	manager, err := os.ReadFile(operatorSrc(t, "config", "manager", "manager.yaml"))
	if err != nil {
		t.Fatalf("read source manager.yaml: %v", err)
	}
	// manager.yaml is a multi-document stream (Namespace + Deployment); the
	// target extracts each document and rewrites "system" to "foundry-system".
	docs := strings.SplitN(string(manager), "\n---\n", 2)
	if len(docs) != 2 {
		t.Fatal("source manager.yaml is missing the document separator before the Deployment document")
	}
	wantDeployment := []byte(strings.ReplaceAll(docs[1], "namespace: system", "namespace: foundry-system"))
	wantNamespace := []byte(strings.ReplaceAll(docs[0], "name: system", "name: foundry-system") + "\n")

	compare := func(name string, want []byte) {
		t.Helper()
		got, err := Manifests.ReadFile("operator/" + name)
		if err != nil {
			t.Errorf("read embedded operator/%s: %v", name, err)
			return
		}
		if !bytes.Equal(got, want) {
			t.Errorf("embedded operator/%s differs from its source; run `make flowctl-manifests`", name)
		}
	}
	compare("deployment.yaml", wantDeployment)
	compare("namespace.yaml", wantNamespace)

	// role.yaml is a verbatim copy of config/rbac/role.yaml (cluster-scoped).
	role, err := os.ReadFile(operatorSrc(t, "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatalf("read source role.yaml: %v", err)
	}
	compare("role.yaml", role)

	// rolebinding.yaml and serviceaccount.yaml are copies of their config/rbac
	// sources with the "system" namespace rewritten to "foundry-system".
	for _, c := range []struct{ embedded, src string }{
		{embedded: "rolebinding.yaml", src: "role_binding.yaml"},
		{embedded: "serviceaccount.yaml", src: "service_account.yaml"},
	} {
		src, err := os.ReadFile(operatorSrc(t, "config", "rbac", c.src))
		if err != nil {
			t.Fatalf("read source %s: %v", c.src, err)
		}
		compare(c.embedded, []byte(strings.ReplaceAll(string(src), "namespace: system", "namespace: foundry-system")))
	}
}
