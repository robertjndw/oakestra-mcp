package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c != Defaults() {
		t.Fatalf("got %+v, want defaults", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := FromEnv(env(map[string]string{
		"OAKESTRA_API_URL":   "http://localhost:10000/",
		"CLUSTER_RA_PORT":    "12000",
		"MCP_ENABLE_WRITES":  "true",
		"CONFIRM_TTL":        "90s",
		"ACTIVITY_RETENTION": "24h",
		"PUBLIC_URL":         "https://root.example/mcp",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.OakestraAPIURL != "http://localhost:10000" || c.ClusterRAPort != 12000 || !c.EnableWrites ||
		c.ConfirmTTL != 90*time.Second || c.ActivityRetention != 24*time.Hour || c.PublicURL != "https://root.example/mcp" {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestInvalidValuesAreAllReported(t *testing.T) {
	_, err := FromEnv(env(map[string]string{
		"OAKESTRA_API_URL":  "system_manager:10000",
		"CLUSTER_RA_PORT":   "70000",
		"MCP_ENABLE_WRITES": "maybe",
		"CONFIRM_TTL":       "-1m",
		"LISTEN_ADDR":       "8090",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, key := range []string{"OAKESTRA_API_URL", "CLUSTER_RA_PORT", "MCP_ENABLE_WRITES", "CONFIRM_TTL", "LISTEN_ADDR"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}
