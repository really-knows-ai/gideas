package manifests_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigMap_FieldPresence(t *testing.T) {
	cms := findConfigMaps(t)
	tests := []struct {
		name        string
		configMap   string
		fields      []string
		rawContains string
	}{
		{name: "facilitator-config/arbiterNode", configMap: "facilitator-config", fields: []string{"arbiterNode"}},
		{name: "arbiter-config/all", configMap: "arbiter-config",
			fields: []string{"jurySize", "jurorNode", "consensusStrategy", "maxRounds", "clerkNode", "hungOutput"}},
		{name: "tribunal-config/all", configMap: "tribunal-config",
			fields: []string{"jurySize", "jurorNode", "consensusStrategy", "maxRounds", "clerkNode", "hungOutput"}},
		{name: "ttl-watcher-config/fields", configMap: "ttl-watcher-config",
			fields: []string{"scanPeriod", "tier1", "tier2"}},
		{name: "hitl-appraisal-config/choiceLabels", configMap: "hitl-appraisal-config",
			fields: []string{"choiceLabels"}},
		{name: "arbiter-hitl-resolve-config/choiceLabels",
			configMap: "arbiter-hitl-resolve-config", fields: []string{"choiceLabels"}},
		{name: "tribunal-hitl-resolve-config/choiceLabels",
			configMap: "tribunal-hitl-resolve-config", fields: []string{"choiceLabels"}},
		{name: "clerk-forge-config/systemPrompt+queryTemplate",
			configMap: "clerk-forge-config", fields: []string{"systemPrompt", "queryTemplate"}},
		{name: "clerk-sort-config/nodeOrder", configMap: "clerk-sort-config", fields: []string{"nodeOrder"}},
		{name: "clerk-done-router-config/rules+tier",
			configMap: "clerk-done-router-config", fields: []string{"rules"}, rawContains: "petition_max_tier"},
		{name: "hitl-gate-config/rules", configMap: "hitl-gate-config", fields: []string{"rules"}},
		{name: "codification-config/codificationNodes",
			configMap: "codification-config", fields: []string{"codificationNodes"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm, ok := cms[tt.configMap]
			if !ok {
				t.Fatalf("ConfigMap %q not found in configmaps.yaml", tt.configMap)
			}
			raw, ok := cm.Data["node-config.yaml"]
			if !ok {
				t.Fatalf("%q missing 'node-config.yaml' key", tt.configMap)
			}
			var cfg map[string]any
			if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatalf("unmarshalling %q config data: %v", tt.configMap, err)
			}
			for _, field := range tt.fields {
				if _, ok := cfg[field]; !ok {
					t.Errorf("%q config missing field %q", tt.configMap, field)
				}
			}
			if tt.rawContains != "" && !strings.Contains(raw, tt.rawContains) {
				t.Errorf("%q config rules should reference %s", tt.configMap, tt.rawContains)
			}
		})
	}
}

func TestConfigMap_FieldValue(t *testing.T) {
	cms := findConfigMaps(t)
	tests := []struct {
		name      string
		configMap string
		field     string
		want      any
	}{
		{name: "facilitator-config/arbiterNode", configMap: "facilitator-config",
			field: "arbiterNode", want: nodeNameArbiter},
		{name: "juror-config/personality", configMap: "juror-config", field: "personality", want: "textualist"},
		{name: "clerk-sort-config/nodeOrder", configMap: "clerk-sort-config", field: "nodeOrder", want: "appraisal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm, ok := cms[tt.configMap]
			if !ok {
				t.Fatalf("ConfigMap %q not found in configmaps.yaml", tt.configMap)
			}
			raw, ok := cm.Data["node-config.yaml"]
			if !ok {
				t.Fatalf("%q missing 'node-config.yaml' key", tt.configMap)
			}
			var cfg map[string]any
			if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatalf("unmarshalling %q config data: %v", tt.configMap, err)
			}
			v, ok := cfg[tt.field]
			if !ok {
				t.Errorf("%q config missing %q field", tt.configMap, tt.field)
			} else if v != tt.want {
				t.Errorf("%q config %s: want %v, got %v", tt.configMap, tt.field, tt.want, v)
			}
		})
	}
}

func TestConfigMap_SystemPromptMentions(t *testing.T) {
	cms := findConfigMaps(t)
	tests := []struct {
		name      string
		configMap string
		contains  string
	}{
		{name: "clerk-refine-config/revising", configMap: "clerk-refine-config", contains: "revising"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm, ok := cms[tt.configMap]
			if !ok {
				t.Fatalf("ConfigMap %q not found in configmaps.yaml", tt.configMap)
			}
			raw, ok := cm.Data["node-config.yaml"]
			if !ok {
				t.Fatalf("%q missing 'node-config.yaml' key", tt.configMap)
			}
			var cfg map[string]any
			if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
				t.Fatalf("unmarshalling %q config data: %v", tt.configMap, err)
			}
			sp, ok := cfg["systemPrompt"]
			if !ok {
				t.Fatalf("%q config missing 'systemPrompt' field", tt.configMap)
			}
			spStr, ok := sp.(string)
			if !ok {
				t.Fatalf("%q config systemPrompt is not a string: %T", tt.configMap, sp)
			}
			if !strings.Contains(spStr, tt.contains) {
				t.Errorf("%q config systemPrompt does not mention %q", tt.configMap, tt.contains)
			}
		})
	}
}
