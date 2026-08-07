package cli

import (
	"testing"

	"sift/internal/config"
)

func TestDeepInfraPriorityConfigValue(t *testing.T) {
	cfg := config.Default()
	if cfg.API.DeepInfraPriority {
		t.Fatal("deepinfra priority should default to false")
	}
	if err := setConfigValue(cfg, "api.deepinfra_priority", "true"); err != nil {
		t.Fatalf("set priority: %v", err)
	}
	got, err := getConfigValue(cfg, "api.deepinfra_priority")
	if err != nil {
		t.Fatalf("get priority: %v", err)
	}
	if got != "true" {
		t.Fatalf("priority=%q want true", got)
	}
}
