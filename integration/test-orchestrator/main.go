package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ── Scenario types ────────────────────────────────────────────────────────────

// Scenario is a single test scenario loaded from a YAML file.
type Scenario struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	InitialState InitialState `yaml:"initial_state"`
	Steps       []Step       `yaml:"steps"`
}

// InitialState describes the cluster state to establish before the scenario runs.
type InitialState struct {
	ActiveValidator string `yaml:"active_validator"`
}

// Step is a single action or assertion within a scenario.
type Step struct {
	Name   string `yaml:"name"`
	Action string `yaml:"action"`

	// used by: wait
	Duration string `yaml:"duration,omitempty"`

	// used by: set_active, disconnect, reconnect, set_unhealthy, set_healthy
	Target string `yaml:"target,omitempty"`

	// used by: assert
	Timeout     string                       `yaml:"timeout,omitempty"`
	State       map[string]ValidatorExpected `yaml:"state,omitempty"`
	ActiveCount *int                         `yaml:"active_count,omitempty"`
}

// ValidatorExpected is the expected state of a single validator in an assert step.
type ValidatorExpected struct {
	Role string `yaml:"role"`
}

// ── Orchestrator ──────────────────────────────────────────────────────────────

type Orchestrator struct {
	mockURL       string
	validatorURLs map[string]string
}

func NewOrchestrator() *Orchestrator {
	return &Orchestrator{
		mockURL: os.Getenv("MOCK_SOLANA_URL"),
		validatorURLs: map[string]string{
			"validator-1": os.Getenv("VALIDATOR_1_URL"),
			"validator-2": os.Getenv("VALIDATOR_2_URL"),
			"validator-3": os.Getenv("VALIDATOR_3_URL"),
		},
	}
}

// ── Scenario loading ──────────────────────────────────────────────────────────

func loadScenarios(dir string) ([]Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading scenarios dir %q: %w", dir, err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)

	var scenarios []Scenario
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %q: %w", path, err)
		}
		var s Scenario
		if err := yaml.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("parsing %q: %w", path, err)
		}
		scenarios = append(scenarios, s)
		log.Printf("loaded scenario: %q from %s", s.Name, filepath.Base(path))
	}
	return scenarios, nil
}

// ── Mock control ──────────────────────────────────────────────────────────────

func (o *Orchestrator) callAction(action, target string) error {
	body, _ := json.Marshal(map[string]string{"action": action, "target": target})
	resp, err := http.Post(o.mockURL+"/action", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST /action %s %s: %w", action, target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /action %s %s: status %d", action, target, resp.StatusCode)
	}
	return nil
}

// resetState brings the cluster to a clean initial state before each scenario.
func (o *Orchestrator) resetState(initialActive string) error {
	if err := o.callAction("reset", initialActive); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	log.Printf("[reset] cluster reset: active_validator=%q", initialActive)
	return nil
}

// ── Metrics polling ───────────────────────────────────────────────────────────

type ValidatorStatus struct {
	Role   string
	Online bool
}

func (o *Orchestrator) getValidatorStatus(name string) (*ValidatorStatus, error) {
	url := o.validatorURLs[name] + "/metrics"
	resp, err := http.Get(url)
	if err != nil {
		return &ValidatorStatus{Online: false}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &ValidatorStatus{Online: false}, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ValidatorStatus{Online: false}, nil
	}
	metrics := string(body)

	role := "unknown"
	if strings.Contains(metrics, `validator_role="active"`) {
		role = "active"
	} else if strings.Contains(metrics, `validator_role="passive"`) {
		role = "passive"
	}

	return &ValidatorStatus{Role: role, Online: true}, nil
}

func (o *Orchestrator) getAllStatuses() map[string]*ValidatorStatus {
	statuses := make(map[string]*ValidatorStatus, len(o.validatorURLs))
	for name := range o.validatorURLs {
		s, _ := o.getValidatorStatus(name)
		statuses[name] = s
	}
	return statuses
}

// ── Step execution ────────────────────────────────────────────────────────────

func (o *Orchestrator) executeStep(step Step) error {
	log.Printf("  step: %s [%s]", step.Name, step.Action)

	switch step.Action {
	case "wait":
		d, err := time.ParseDuration(step.Duration)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", step.Duration, err)
		}
		log.Printf("  waiting %s...", d)
		time.Sleep(d)
		return nil

	case "set_active", "disconnect", "reconnect", "set_unhealthy", "set_healthy":
		return o.callAction(step.Action, step.Target)

	case "assert":
		return o.executeAssert(step)

	default:
		return fmt.Errorf("unknown action: %q", step.Action)
	}
}

func (o *Orchestrator) executeAssert(step Step) error {
	timeout := 30 * time.Second
	if step.Timeout != "" {
		d, err := time.ParseDuration(step.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout %q: %w", step.Timeout, err)
		}
		timeout = d
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		statuses := o.getAllStatuses()

		// Check active_count condition
		activeCountOK := true
		if step.ActiveCount != nil {
			count := 0
			for _, s := range statuses {
				if s.Role == "active" {
					count++
				}
			}
			activeCountOK = count == *step.ActiveCount
		}

		// Check per-validator state conditions
		stateOK := true
		for name, expected := range step.State {
			s, ok := statuses[name]
			if !ok || (expected.Role != "" && s.Role != expected.Role) {
				stateOK = false
				break
			}
		}

		if activeCountOK && stateOK {
			// Log final passing state
			var parts []string
			for name, s := range statuses {
				parts = append(parts, fmt.Sprintf("%s=%s", name, s.Role))
			}
			sort.Strings(parts)
			log.Printf("  assert passed: %s", strings.Join(parts, " "))
			return nil
		}

		// Log current state for visibility while waiting
		var parts []string
		for name, s := range statuses {
			marker := ""
			if exp, ok := step.State[name]; ok && exp.Role != "" && s.Role != exp.Role {
				marker = fmt.Sprintf("(want %s)", exp.Role)
			}
			parts = append(parts, fmt.Sprintf("%s=%s%s", name, s.Role, marker))
		}
		sort.Strings(parts)
		if step.ActiveCount != nil {
			count := 0
			for _, s := range statuses {
				if s.Role == "active" {
					count++
				}
			}
			parts = append(parts, fmt.Sprintf("active_count=%d(want %d)", count, *step.ActiveCount))
		}
		log.Printf("  waiting... %s", strings.Join(parts, " "))
		time.Sleep(2 * time.Second)
	}

	// Build descriptive failure message
	statuses := o.getAllStatuses()
	var parts []string
	for name, s := range statuses {
		parts = append(parts, fmt.Sprintf("%s=%s", name, s.Role))
	}
	sort.Strings(parts)
	return fmt.Errorf("assert timed out after %s: %s", timeout, strings.Join(parts, " "))
}

// ── Scenario runner ───────────────────────────────────────────────────────────

func (o *Orchestrator) runScenario(s Scenario) error {
	log.Printf("=== scenario: %s ===", s.Name)
	if s.Description != "" {
		log.Printf("    %s", s.Description)
	}

	// Reset cluster to a clean initial state before the scenario starts.
	if err := o.resetState(s.InitialState.ActiveValidator); err != nil {
		return fmt.Errorf("initial reset failed: %w", err)
	}

	// Allow validators to pick up the new state before running any steps.
	log.Printf("  stabilising for 15s...")
	time.Sleep(15 * time.Second)

	for i, step := range s.Steps {
		if err := o.executeStep(step); err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, step.Name, err)
		}
	}
	return nil
}

func (o *Orchestrator) runAll(scenarios []Scenario) (passed, failed int) {
	for _, s := range scenarios {
		err := o.runScenario(s)
		if err != nil {
			log.Printf("❌ FAIL: %s — %v", s.Name, err)
			failed++
		} else {
			log.Printf("✅ PASS: %s", s.Name)
			passed++
		}
		// Brief pause between scenarios so validators settle before the next reset.
		time.Sleep(5 * time.Second)
	}
	return passed, failed
}

// ── Readiness wait ────────────────────────────────────────────────────────────

func (o *Orchestrator) waitForServices(timeout time.Duration) error {
	log.Printf("waiting for services to be ready (up to %s)...", timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready := true
		// Check mock
		if resp, err := http.Get(o.mockURL + "/public-ip"); err != nil || resp.StatusCode != http.StatusOK {
			ready = false
		}
		// Check all validators
		for name := range o.validatorURLs {
			if s, _ := o.getValidatorStatus(name); !s.Online {
				ready = false
			}
		}
		if ready {
			log.Printf("all services ready")
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("services not ready after %s", timeout)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	scenariosDir := os.Getenv("SCENARIOS_DIR")
	if scenariosDir == "" {
		scenariosDir = "./scenarios"
	}

	scenarios, err := loadScenarios(scenariosDir)
	if err != nil {
		log.Fatalf("failed to load scenarios: %v", err)
	}
	if len(scenarios) == 0 {
		log.Fatalf("no scenario YAML files found in %q", scenariosDir)
	}

	orchestrator := NewOrchestrator()

	if err := orchestrator.waitForServices(2 * time.Minute); err != nil {
		log.Fatalf("services not ready: %v", err)
	}

	// Extra settling time after services are up before running scenarios.
	log.Printf("giving validators 10s to initialise before starting scenarios...")
	time.Sleep(10 * time.Second)

	passed, failed := orchestrator.runAll(scenarios)

	log.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Printf("results: %d passed, %d failed, %d total", passed, failed, passed+failed)

	if failed > 0 {
		log.Printf("❌ Integration test failed: %d scenario(s) did not pass", failed)
		os.Exit(1)
	}

	log.Printf("✅ Integration test completed successfully!")
}
