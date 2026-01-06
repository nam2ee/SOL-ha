package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDelinquentSlotDistanceOverride_LoadsFromYAML(t *testing.T) {
	// Create a temporary YAML file
	yamlContent := `failover:
  delinquent_slot_distance_override:
    enabled: true
    value: 1000
  poll_interval_duration: 5s
  leaderless_samples_threshold: 3
  takeover_jitter_duration: 3s
  peers:
    validator-1:
      ip: 192.168.1.10
  active:
    command: echo
  passive:
    command: echo
`

	tmpFile, err := os.CreateTemp("", "test-config-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(yamlContent); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	// Load the config (just load, don't initialize to avoid identity file requirements)
	cfg, err := New(NewConfigParams{})
	if err != nil {
		t.Fatalf("Failed to create config: %v", err)
	}

	if err := cfg.LoadFromFile(tmpFile.Name()); err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Check that the delinquent slot distance override was loaded BEFORE Initialize
	assert.True(t, cfg.Failover.DelinquentSlotDistanceOverride.Enabled, "Enabled should be true")
	assert.Equal(t, uint64(1000), cfg.Failover.DelinquentSlotDistanceOverride.Value, "Value should be 1000")
}

