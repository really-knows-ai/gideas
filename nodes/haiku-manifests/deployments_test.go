package manifests_test

import "testing"

func TestDeployment_SidecarEnvVars(t *testing.T) {
	deps := findDeployments(t)
	tests := []struct {
		name               string
		nodeID             string
		envName            string
		expectValue        string
		expectPresenceOnly bool
	}{
		{name: "sort/LIBRARIAN_ADDRESS", nodeID: "sort", envName: envLibrarianAddress,
			expectValue: addrLibrarian},
		{name: "facilitator/LIBRARIAN_ADDRESS", nodeID: "facilitator",
			envName: envLibrarianAddress, expectValue: addrLibrarian},
		{name: "facilitator/FRICTION_LEDGER_ADDRESS", nodeID: "facilitator",
			envName: "FRICTION_LEDGER_ADDRESS", expectValue: addrFrictionLedger},
		{name: "tribunal/LIBRARIAN_ADDRESS", nodeID: "tribunal",
			envName: envLibrarianAddress, expectValue: addrLibrarian},
		{name: "tribunal/FRICTION_LEDGER_ADDRESS", nodeID: "tribunal",
			envName: "FRICTION_LEDGER_ADDRESS", expectValue: addrFrictionLedger},
		{name: "friction-watcher/EVENT_BUS_ADDRESS", nodeID: "friction-watcher",
			envName: "EVENT_BUS_ADDRESS", expectValue: "flow-eventbus:50056"},
		{name: "ttl-watcher/LIBRARIAN_ADDRESS", nodeID: "ttl-watcher",
			envName: envLibrarianAddress, expectValue: addrLibrarian},
		{name: "clerk-sort/LIBRARIAN_ADDRESS", nodeID: "clerk-sort",
			envName: envLibrarianAddress, expectValue: addrLibrarian},
		{name: "clerk-appraisal/LIBRARIAN_ADDRESS", nodeID: "clerk-appraisal",
			envName: envLibrarianAddress, expectPresenceOnly: true},
		{name: "clerk-appraisal/EVENT_BUS_ADDRESS", nodeID: "clerk-appraisal",
			envName: "EVENT_BUS_ADDRESS", expectPresenceOnly: true},
		{name: "clerk-appraisal/FRICTION_LEDGER_ADDRESS", nodeID: "clerk-appraisal",
			envName: "FRICTION_LEDGER_ADDRESS", expectPresenceOnly: true},
		{name: "clerk-facilitator/LIBRARIAN_ADDRESS", nodeID: "clerk-facilitator",
			envName: envLibrarianAddress, expectValue: addrLibrarian},
		{name: "clerk-facilitator/FRICTION_LEDGER_ADDRESS", nodeID: "clerk-facilitator",
			envName: "FRICTION_LEDGER_ADDRESS", expectValue: addrFrictionLedger},
		{name: "law-applicator/LIBRARIAN_ADDRESS", nodeID: "law-applicator",
			envName: envLibrarianAddress, expectValue: addrLibrarian},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := deps[tt.nodeID]
			if !ok {
				t.Fatalf("Deployment with FLOW_NODE_ID=%q not found in deployments.yaml", tt.nodeID)
			}
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == containerNameSidecar {
					if tt.expectPresenceOnly {
						for _, e := range c.Env {
							if e.Name == tt.envName {
								return
							}
						}
						t.Errorf("%q sidecar missing %s env var", tt.nodeID, tt.envName)
					} else {
						for _, e := range c.Env {
							if e.Name == tt.envName {
								if e.Value != tt.expectValue {
									t.Errorf("%s: want %q, got %q", tt.envName, tt.expectValue, e.Value)
								}
								return
							}
						}
						t.Errorf("%q sidecar missing %s env var", tt.nodeID, tt.envName)
					}
					return
				}
			}
			t.Errorf("%q Deployment missing sidecar container", tt.nodeID)
		})
	}
}

func TestDeployment_NodeEnvVars(t *testing.T) {
	deps := findDeployments(t)
	tests := []struct {
		name    string
		nodeID  string
		envName string
	}{
		{name: "juror/OLLAMA_BASE_URL", nodeID: "juror", envName: envOllamaBaseURL},
		{name: "clerk-forge/OLLAMA_BASE_URL", nodeID: "clerk-forge", envName: envOllamaBaseURL},
		{name: "codify-smt/OLLAMA_BASE_URL", nodeID: "codify-smt", envName: envOllamaBaseURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := deps[tt.nodeID]
			if !ok {
				t.Fatalf("Deployment with FLOW_NODE_ID=%q not found in deployments.yaml", tt.nodeID)
			}
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == containerNameNode {
					for _, e := range c.Env {
						if e.Name == tt.envName {
							return
						}
					}
					t.Errorf("%q node container missing %s env var", tt.nodeID, tt.envName)
					return
				}
			}
			t.Errorf("%q Deployment missing node container", tt.nodeID)
		})
	}
}

func TestDeployment_NodeImage(t *testing.T) {
	deps := findDeployments(t)
	tests := []struct {
		name   string
		nodeID string
		image  string
	}{
		{name: "hitl-appraisal", nodeID: "hitl-appraisal", image: "hitl:latest"},
		{name: "arbiter-hitl-resolve", nodeID: "arbiter-hitl-resolve", image: "hitl:latest"},
		{name: "tribunal-hitl-resolve", nodeID: "tribunal-hitl-resolve", image: "hitl:latest"},
		{name: "clerk-forge", nodeID: "clerk-forge", image: "forge:latest"},
		{name: "clerk-done-router", nodeID: "clerk-done-router", image: "rule-router:latest"},
		{name: "hitl-gate", nodeID: "hitl-gate", image: "rule-router:latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := deps[tt.nodeID]
			if !ok {
				t.Fatalf("Deployment with FLOW_NODE_ID=%q not found in deployments.yaml", tt.nodeID)
			}
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == containerNameNode {
					if c.Image != tt.image {
						t.Errorf("%q node container image: want %q, got %q", tt.nodeID, tt.image, c.Image)
					}
					break
				}
			}
		})
	}
}

func TestDeployment_ConfigMapMount(t *testing.T) {
	deps := findDeployments(t)
	tests := []struct {
		name          string
		nodeID        string
		volumeName    string
		configMapName string
	}{
		{name: "arbiter", nodeID: "arbiter", volumeName: "node-config", configMapName: "arbiter-config"},
		{name: "codification", nodeID: "codification", volumeName: "node-config", configMapName: "codification-config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := deps[tt.nodeID]
			if !ok {
				t.Fatalf("Deployment with FLOW_NODE_ID=%q not found in deployments.yaml", tt.nodeID)
			}
			for _, vol := range d.Spec.Template.Spec.Volumes {
				if vol.Name == tt.volumeName && vol.ConfigMap != nil {
					if vol.ConfigMap.Name != tt.configMapName {
						t.Errorf("%q volume configMap name: want %q, got %q",
							tt.nodeID, tt.configMapName, vol.ConfigMap.Name)
					}
					return
				}
			}
			t.Errorf("%q Deployment missing %s volume with %s ConfigMap",
				tt.nodeID, tt.volumeName, tt.configMapName)
		})
	}
}

func TestDeployment_NoConfigMapMount(t *testing.T) {
	deps := findDeployments(t)
	tests := []struct {
		name   string
		nodeID string
	}{
		{name: "friction-watcher", nodeID: "friction-watcher"},
		{name: "law-applicator", nodeID: "law-applicator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := deps[tt.nodeID]
			if !ok {
				t.Fatalf("Deployment with FLOW_NODE_ID=%q not found in deployments.yaml", tt.nodeID)
			}
			if len(d.Spec.Template.Spec.Volumes) != 0 {
				t.Errorf("%q Deployment should have no volumes, got %d",
					tt.nodeID, len(d.Spec.Template.Spec.Volumes))
			}
		})
	}
}
