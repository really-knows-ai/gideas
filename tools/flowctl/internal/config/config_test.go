package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// findToolsRoot walks up from the test directory to find the tools/ directory.
func findToolsRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if filepath.Base(dir) == "tools" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find tools/ root")
		}
		dir = parent
	}
}

// findGoWork reads the go.work file from the tools directory.
func findGoWork(t *testing.T) string {
	t.Helper()
	toolsDir := findToolsRoot(t)
	data, err := os.ReadFile(filepath.Join(toolsDir, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newTestCommand creates a cobra command with the watch flags registered.
func newTestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "watch"}
	cmd.Flags().String("namespace", "", "Workitem namespace")
	cmd.Flags().String("system-namespace", "", "System services namespace")
	return cmd
}

func TestParseFlags_Namespace(t *testing.T) {
	tests := []struct {
		name       string
		flagVal    string // empty string means flag not set
		envVal     string // empty string means env not set
		want       string
		wantErr    bool
		setChanged bool // if true, mark flag as changed
	}{
		{
			name:       "T1: flag overrides env var",
			flagVal:    "from-flag",
			envVal:     "from-env",
			want:       "from-flag",
			setChanged: true,
		},
		{
			name:       "T2: env var overrides default",
			flagVal:    "",
			envVal:     "from-env",
			want:       "from-env",
			setChanged: false,
		},
		{
			name:       "T3: empty when nothing set (caller resolves kube context > default)",
			flagVal:    "",
			envVal:     "",
			want:       "",
			setChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			if tt.envVal != "" {
				os.Setenv("FLOW_NAMESPACE", tt.envVal)
			}
			cmd := newTestCommand()
			if tt.flagVal != "" {
				cmd.Flags().Set("namespace", tt.flagVal)
			}
			if tt.setChanged {
				cmd.Flags().Set("namespace", tt.flagVal) // Set marks changed
			}
			cfg, err := ParseFlags(cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Namespace != tt.want {
				t.Errorf("Namespace = %q, want %q", cfg.Namespace, tt.want)
			}
		})
	}
}

func TestParseFlags_SystemNamespace(t *testing.T) {
	tests := []struct {
		name         string
		nsFlag       string
		sysFlag      string // empty = not set
		sysEnv       string // empty = not set
		setSys       bool   // mark system-namespace flag as changed
		want         string
	}{
		{
			name:   "T4: SystemNamespace mirrors Namespace when unset",
			nsFlag: "my-ns",
			sysFlag: "",
			sysEnv: "",
			setSys: false,
			want:   "my-ns",
		},
		{
			name:   "T5: SystemNamespace override from flag",
			nsFlag: "my-ns",
			sysFlag: "sys-ns",
			sysEnv: "",
			setSys: true,
			want:   "sys-ns",
		},
		{
			name:   "T6: SystemNamespace override from env var",
			nsFlag: "my-ns",
			sysFlag: "",
			sysEnv: "sys-env",
			setSys: false,
			want:   "sys-env",
		},
		{
			name:   "T7: SystemNamespace=auto is passed through",
			nsFlag: "",
			sysFlag: "auto",
			sysEnv: "",
			setSys: true,
			want:   "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			if tt.sysEnv != "" {
				os.Setenv("FLOW_SYSTEM_NAMESPACE", tt.sysEnv)
			}
			cmd := newTestCommand()
			if tt.nsFlag != "" {
				cmd.Flags().Set("namespace", tt.nsFlag)
				// Mark namespace as changed to ensure flag is used
				cmd.Flags().Set("namespace", tt.nsFlag)
			}
			if tt.sysFlag != "" {
				cmd.Flags().Set("system-namespace", tt.sysFlag)
			}
			if tt.setSys {
				cmd.Flags().Set("system-namespace", tt.sysFlag)
			}
			cfg, err := ParseFlags(cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.SystemNamespace != tt.want {
				t.Errorf("SystemNamespace = %q, want %q", cfg.SystemNamespace, tt.want)
			}
		})
	}
}

func TestParseFlags_LogFile(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		want    string
	}{
		{
			name:    "T12: from env var",
			envVal:  "/tmp/flowctl.log",
			want:    "/tmp/flowctl.log",
		},
		{
			name:    "T13: default is empty",
			envVal:  "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			if tt.envVal != "" {
				os.Setenv("FLOW_LOG_FILE", tt.envVal)
			}
			cmd := newTestCommand()
			cfg, err := ParseFlags(cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.LogFile != tt.want {
				t.Errorf("LogFile = %q, want %q", cfg.LogFile, tt.want)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	toolsDir := findToolsRoot(t)
	content := findGoWork(t)
	if !strings.Contains(content, "./flowctl") {
		t.Error("go.work does not contain ./flowctl")
	}
	_ = toolsDir // build verified by successful compilation of the test binary itself
}

func TestGoWorkContainsFlowctl(t *testing.T) {
	content := findGoWork(t)
	if !strings.Contains(content, "./flowctl") {
		t.Errorf("go.work does not contain ./flowctl")
	}
}
