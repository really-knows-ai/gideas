package flow

import (
	"strings"
	"testing"
	"text/template"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — Construction
// ---------------------------------------------------------------------------

func TestNewAgent_ValidConstruction(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-001")

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err != nil {
		t.Fatalf("NewAgent() with valid options returned error: %v", err)
	}
	if agent == nil {
		t.Fatal("NewAgent() returned nil agent")
	}
}

func TestNewAgent_InvalidSchemaJSON(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-002")

	_, err := NewAgent(env.client,
		WithSchema([]byte(invalidSchemaJSON)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err == nil {
		t.Fatal("NewAgent() with invalid JSON should return error")
	}
	if !strings.Contains(err.Error(), "invalid output schema") {
		t.Fatalf("expected 'invalid output schema' in error, got: %v", err)
	}
}

func TestNewAgent_NilClient(t *testing.T) {
	_, err := NewAgent(nil,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(template.Must(template.New("q").Parse("{{.Input}}"))),
	)
	if err == nil {
		t.Fatal("NewAgent() with nil client should return error")
	}
	if !strings.Contains(err.Error(), "client must not be nil") {
		t.Fatalf("expected 'client must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_EmptyModelName(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-003")

	_, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName(""),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err == nil {
		t.Fatal("NewAgent() with empty model name should return error")
	}
	if !strings.Contains(err.Error(), "model name must not be empty") {
		t.Fatalf("expected 'model name must not be empty' in error, got: %v", err)
	}
}

func TestNewAgent_MissingSchema(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-004")

	_, err := NewAgent(env.client,
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err == nil {
		t.Fatal("NewAgent() with nil schema should return error")
	}
	if !strings.Contains(err.Error(), "schema must not be nil") {
		t.Fatalf("expected 'schema must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_MissingQueryTemplate(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-005")

	_, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
	)
	if err == nil {
		t.Fatal("NewAgent() with nil query template should return error")
	}
	if !strings.Contains(err.Error(), "query template must not be nil") {
		t.Fatalf("expected 'query template must not be nil' in error, got: %v", err)
	}
}

func TestNewAgent_DefaultHeartbeatInterval(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-007")

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
	)
	if err != nil {
		t.Fatalf("NewAgent() returned error: %v", err)
	}
	if agent.cfg.heartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("expected default heartbeat interval %v, got %v",
			DefaultHeartbeatInterval, agent.cfg.heartbeatInterval)
	}
}

func TestNewAgent_CustomHeartbeatInterval(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-008")

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
		withHeartbeatInterval(5*time.Second),
	)
	if err != nil {
		t.Fatalf("NewAgent() returned error: %v", err)
	}
	if agent.cfg.heartbeatInterval != 5*time.Second {
		t.Fatalf("expected heartbeat interval 5s, got %v", agent.cfg.heartbeatInterval)
	}
}

func TestNewAgent_CustomOutputValidationRetries(t *testing.T) {
	env := setupAgentTestEnv(t, "wid-agent-retries")

	agent, err := NewAgent(env.client,
		WithSchema([]byte(validHaikuSchema)),
		WithModelName("test-model"),
		WithQueryTemplate(simpleQueryTemplate(t)),
		WithOutputValidationRetries(2),
	)
	if err != nil {
		t.Fatalf("NewAgent() returned error: %v", err)
	}
	if agent.cfg.validationRetries != 2 {
		t.Fatalf("expected validation retries 2, got %d", agent.cfg.validationRetries)
	}
}
