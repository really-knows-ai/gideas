package manifests_test

import (
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestManifest_CrossCuttingConsistency(t *testing.T) {
	nodes := findFoundryNodes(t)
	deps := findDeployments(t)
	cms := findConfigMaps(t)
	ff := findFoundryFlow(t)

	t.Run("output_targets_exist", func(t *testing.T) {
		for name, fn := range nodes {
			for _, o := range fn.Spec.Outputs {
				if o.Target == targetEmbassy {
					continue
				}
				if _, ok := nodes[o.Target]; !ok {
					t.Errorf("FoundryNode %q output %q references target %q which does not exist as a FoundryNode",
						name, o.Name, o.Target)
				}
			}
		}
	})

	t.Run("nodegroup_members_exist", func(t *testing.T) {
		for groupName, group := range ff.Spec.NodeGroups {
			for _, member := range group.Nodes {
				if _, ok := nodes[member]; !ok {
					t.Errorf("nodeGroup %q lists %q but no FoundryNode with that name exists",
						groupName, member)
				}
			}
		}
	})

	t.Run("configmap_consistency", func(t *testing.T) {
		for nodeID, d := range deps {
			for _, vol := range d.Spec.Template.Spec.Volumes {
				if vol.ConfigMap == nil {
					continue
				}
				if _, ok := cms[vol.ConfigMap.Name]; !ok {
					t.Errorf("Deployment %q (FLOW_NODE_ID=%q) mounts ConfigMap %q but it does not exist in configmaps.yaml",
						d.Metadata.Name, nodeID, vol.ConfigMap.Name)
				}
			}
		}
	})

	t.Run("deployment_crd_alignment", func(t *testing.T) {
		for nodeID, node := range nodes {
			if node.Spec.Capabilities == nil {
				t.Errorf("FoundryNode %q has no capabilities", nodeID)
			}
		}
	})

	t.Run("no_duplicate_foundrynode_names", func(t *testing.T) {
		docs := parseMultiDocYAML(t, "flow.yaml")
		seen := make(map[string]int)
		for _, doc := range docs {
			var obj k8sObject
			if err := yaml.Unmarshal(doc, &obj); err != nil {
				t.Fatalf("unmarshalling k8sObject: %v", err)
			}
			if obj.Kind == "FoundryNode" {
				seen[obj.Metadata.Name]++
			}
		}
		for name, count := range seen {
			if count > 1 {
				t.Errorf("FoundryNode %q appears %d times in flow.yaml (must be unique)", name, count)
			}
		}
	})

	t.Run("embassy_exclusion", func(t *testing.T) {
		if _, ok := nodes[targetEmbassy]; ok {
			t.Errorf("%q must NOT appear as a FoundryNode in flow.yaml (it is operator-provisioned)", targetEmbassy)
		}
	})

	t.Run("foundrynode_count", func(t *testing.T) {
		if len(nodes) != 6 {
			names := make([]string, 0, len(nodes))
			for n := range nodes {
				names = append(names, n)
			}
			sort.Strings(names)
			t.Errorf("expected 6 FoundryNode documents, got %d: %v", len(nodes), names)
		}
	})

	t.Run("deployment_count", func(t *testing.T) {
		for name := range nodes {
			if _, ok := deps[name]; !ok {
				t.Errorf("FoundryNode %q has no matching Deployment (by FLOW_NODE_ID)", name)
			}
		}
		if len(deps) != 25 {
			ids := make([]string, 0, len(deps))
			for id := range deps {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			t.Errorf("expected 25 Deployments, got %d: %v", len(deps), ids)
		}
	})
}
