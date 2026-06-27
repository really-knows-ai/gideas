package manifests_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	containerNameSidecar = "sidecar"
	containerNameNode    = "node"
	nodeNameArbiter      = "arbiter"
	nodeNameTribunal     = "tribunal"
	addrLibrarian        = "flow-librarian:50058"
	addrFrictionLedger   = "flow-frictionledger:50057"
	envLibrarianAddress  = "LIBRARIAN_ADDRESS"
	envOllamaBaseURL     = "OLLAMA_BASE_URL"
	outputNameDefault    = "default"
	targetClerkSort      = "clerk-sort"
	targetEmbassy        = "embassy"
)

type k8sObject struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
}

type nodeGroupSpec struct {
	Nodes []string `yaml:"nodes"`
}

type foundryFlowSpec struct {
	EntryContracts   map[string]map[string][]string `yaml:"entryContracts"`
	ExitContracts    map[string]map[string][]string `yaml:"exitContracts"`
	GovernancePolicy map[string]any                 `yaml:"governancePolicy"`
	NodeGroups       map[string]nodeGroupSpec       `yaml:"nodeGroups"`
}

type foundryFlow struct {
	k8sObject `yaml:",inline"`
	Spec      foundryFlowSpec `yaml:"spec"`
}

type foundryNodeOutput struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
}

type foundryNodeSpec struct {
	Image        string              `yaml:"image"`
	Entry        string              `yaml:"entry,omitempty"`
	Exit         string              `yaml:"exit,omitempty"`
	Outputs      []foundryNodeOutput `yaml:"outputs"`
	Capabilities []string            `yaml:"capabilities"`
}

type foundryNode struct {
	k8sObject `yaml:",inline"`
	Spec      foundryNodeSpec `yaml:"spec"`
}

type envVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type volumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	ReadOnly  bool   `yaml:"readOnly"`
}

type deploymentContainer struct {
	Name         string        `yaml:"name"`
	Image        string        `yaml:"image"`
	Env          []envVar      `yaml:"env"`
	VolumeMounts []volumeMount `yaml:"volumeMounts"`
}

type configMapVolumeSource struct {
	Name string `yaml:"name"`
}

type volume struct {
	Name      string                 `yaml:"name"`
	ConfigMap *configMapVolumeSource `yaml:"configMap,omitempty"`
}

type deploymentSpec struct {
	Template struct {
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
		Spec struct {
			Containers []deploymentContainer `yaml:"containers"`
			Volumes    []volume              `yaml:"volumes"`
		} `yaml:"spec"`
	} `yaml:"template"`
}

type deployment struct {
	k8sObject `yaml:",inline"`
	Spec      deploymentSpec `yaml:"spec"`
}

type governedArtefactSpec struct {
	Stamps []string `yaml:"stamps"`
}

type governedArtefact struct {
	k8sObject `yaml:",inline"`
	Spec      governedArtefactSpec `yaml:"spec"`
}

type configMap struct {
	k8sObject `yaml:",inline"`
	Data      map[string]string `yaml:"data"`
}

func parseMultiDocYAML(t *testing.T, path string) [][]byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var docs [][]byte
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var raw yaml.Node
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decoding YAML from %s: %v", path, err)
		}
		b, err := yaml.Marshal(&raw)
		if err != nil {
			t.Fatalf("re-marshalling YAML node: %v", err)
		}
		docs = append(docs, b)
	}
	return docs
}

func findFoundryFlow(t *testing.T) foundryFlow {
	t.Helper()

	docs := parseMultiDocYAML(t, "flow.yaml")
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "FoundryFlow" {
			var ff foundryFlow
			if err := yaml.Unmarshal(doc, &ff); err != nil {
				t.Fatalf("unmarshalling FoundryFlow: %v", err)
			}
			return ff
		}
	}
	t.Fatal("no FoundryFlow document found in flow.yaml")
	return foundryFlow{}
}

func findGovernedArtefacts(t *testing.T) map[string]governedArtefact {
	t.Helper()

	docs := parseMultiDocYAML(t, "flow.yaml")
	gas := make(map[string]governedArtefact)
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "GovernedArtefact" {
			var ga governedArtefact
			if err := yaml.Unmarshal(doc, &ga); err != nil {
				t.Fatalf("unmarshalling GovernedArtefact %q: %v", obj.Metadata.Name, err)
			}
			gas[ga.Metadata.Name] = ga
		}
	}
	return gas
}

func findFoundryNodes(t *testing.T) map[string]foundryNode {
	t.Helper()

	docs := parseMultiDocYAML(t, "flow.yaml")
	nodes := make(map[string]foundryNode)
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "FoundryNode" {
			var fn foundryNode
			if err := yaml.Unmarshal(doc, &fn); err != nil {
				t.Fatalf("unmarshalling FoundryNode %q: %v", obj.Metadata.Name, err)
			}
			nodes[fn.Metadata.Name] = fn
		}
	}
	return nodes
}

func findDeployments(t *testing.T) map[string]deployment {
	t.Helper()

	docs := parseMultiDocYAML(t, "deployments.yaml")
	deps := make(map[string]deployment)
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "Deployment" {
			var d deployment
			if err := yaml.Unmarshal(doc, &d); err != nil {
				t.Fatalf("unmarshalling Deployment %q: %v", obj.Metadata.Name, err)
			}
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == containerNameSidecar {
					for _, e := range c.Env {
						if e.Name == "FLOW_NODE_ID" {
							deps[e.Value] = d
							break
						}
					}
				}
			}
		}
	}
	return deps
}

func findConfigMaps(t *testing.T) map[string]configMap {
	t.Helper()

	docs := parseMultiDocYAML(t, "configmaps.yaml")
	cms := make(map[string]configMap)
	for _, doc := range docs {
		var obj k8sObject
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			t.Fatalf("unmarshalling k8sObject: %v", err)
		}
		if obj.Kind == "ConfigMap" {
			var cm configMap
			if err := yaml.Unmarshal(doc, &cm); err != nil {
				t.Fatalf("unmarshalling ConfigMap %q: %v", obj.Metadata.Name, err)
			}
			cms[cm.Metadata.Name] = cm
		}
	}
	return cms
}

func assertGroupEqual(t *testing.T, groups map[string]nodeGroupSpec, name string, want []string) {
	t.Helper()

	group, ok := groups[name]
	if !ok {
		t.Errorf("nodeGroups missing %q", name)
		return
	}
	got := group.Nodes

	if len(got) != len(want) {
		t.Errorf("nodeGroups[%q]: want %d entries, got %d\n  want: %v\n  got:  %v",
			name, len(want), len(got), want, got)
		return
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("nodeGroups[%q][%d]: want %q, got %q", name, i, want[i], got[i])
		}
	}

	seen := make(map[string]bool)
	for _, n := range got {
		if seen[n] {
			t.Errorf("nodeGroups[%q] has duplicate entry %q", name, n)
		}
		seen[n] = true
	}
}
