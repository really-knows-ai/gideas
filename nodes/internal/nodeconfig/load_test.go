package nodeconfig

import (
	"fmt"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

func TestParseConsensusStrategy(t *testing.T) {
	tests := []struct {
		input string
		want  flowv1.ConsensusStrategy
	}{
		{"", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY},
		{"SIMPLE_MAJORITY", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY},
		{"SUPER_MAJORITY", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY},
		{"UNANIMITY", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY},
		{"  super_majority  ", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY},
		{"unknown", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%q", tt.input), func(t *testing.T) {
			got := ParseConsensusStrategy(tt.input)
			if got != tt.want {
				t.Fatalf("ParseConsensusStrategy(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
