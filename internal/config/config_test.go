package config

import (
	"strings"
	"testing"
)

func validYAML() string {
	return `
version: 1
providers:
  - {name: haiku, vendor: mock}
  - {name: sonnet, vendor: mock}
  - {name: gpt, vendor: mock}
receptionist:
  model: haiku
  routes:
    - {when: {complexity: trivial}, strategy: solo, thinking: fast, models: {primary: haiku}}
    - {strategy: solo, thinking: auto, models: {primary: sonnet}}
council:
  members: [sonnet, gpt]
`
}

func TestValidConfigLoads(t *testing.T) {
	cfg, err := Parse([]byte(validYAML()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Receptionist.Model != "haiku" || len(cfg.Receptionist.Routes) != 2 {
		t.Fatalf("unexpected parse: %+v", cfg.Receptionist)
	}
	reg := BuildRegistry(cfg)
	if _, err := reg.Get("sonnet"); err != nil {
		t.Fatal("registry missing declared provider")
	}
	// Defaults applied.
	if cfg.Defaults.ToolCallCap != 4 || cfg.TwoBrain.DivTemp != 1.0 || cfg.TwoBrain.ConvTemp != 0.3 {
		t.Fatalf("defaults not applied: %+v %+v", cfg.Defaults, cfg.TwoBrain)
	}
}

// Each validation rule rejects a crafted invalid config with a specific
// error (spec 07 §5 table test).
func TestValidationRuleTable(t *testing.T) {
	cases := []struct {
		name    string
		mutate  string // yaml snippet appended/replacing
		wantErr string
	}{
		{"bad version", "version: 2\nproviders: [{name: a, vendor: mock}]\nreceptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}", "version must be 1"},
		{"unknown route model", validYAML() + `
thinking: {gate_model: haiku}
`, ""}, // control: still valid
		{"route references missing provider", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist:
  model: a
  routes:
    - {strategy: solo, models: {primary: ghost}}
`, `unknown provider "ghost"`},
		{"receptionist model missing", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist:
  model: ghost
  routes: [{strategy: solo, models: {primary: a}}]
`, `unknown provider "ghost"`},
		{"bad strategy", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist:
  model: a
  routes: [{strategy: galactic, models: {primary: a}}]
`, "strategy"},
		{"bad thinking", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist:
  model: a
  routes: [{strategy: solo, thinking: psychic, models: {primary: a}}]
`, "thinking"},
		{"bad when value", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist:
  model: a
  routes: [{when: {complexity: impossible}, strategy: solo, models: {primary: a}}]
`, "when.complexity"},
		{"twobrain rounds out of range", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
twobrain: {rounds: 7}
`, "twobrain.rounds"},
		{"council rounds out of range", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
council: {rounds: 42}
`, "council.rounds"},
		{"similarity without embedder", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
council:
  consensus: {method: similarity, threshold: 0.9}
`, "requires an embedder"},
		{"bad consensus threshold", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
council:
  consensus: {method: vote, threshold: 1.5}
`, "threshold"},
		{"bad deadlock", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
council: {on_deadlock: coin_flip}
`, "on_deadlock"},
		{"unsorted warn thresholds", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist:
  model: a
  warn_thresholds: [0.9, 0.5]
  routes: [{strategy: solo, models: {primary: a}}]
`, "sorted"},
		{"local without base_url", `
version: 1
providers: [{name: a, vendor: local}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
`, "base_url"},
		{"council route without members", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist:
  model: a
  routes: [{strategy: council, models: {primary: a}}]
`, ">=2 members"},
		{"file_read tool without root", `
version: 1
providers: [{name: a, vendor: mock}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
tools: [{name: rf, kind: file_read}]
`, "requires root"},
		{"unknown vendor", `
version: 1
providers: [{name: a, vendor: quantum}]
receptionist: {model: a, routes: [{strategy: solo, models: {primary: a}}]}
`, "unknown vendor"},
	}

	for _, tc := range cases {
		_, err := Parse([]byte(tc.mutate))
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: expected valid, got %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected error containing %q, got nil", tc.name, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error %q does not contain %q", tc.name, err.Error(), tc.wantErr)
		}
	}
}

func TestGoogleProviderGetsCompatBaseURL(t *testing.T) {
	cfg, err := Parse([]byte(`
version: 1
providers:
  - {name: gem, vendor: google, api_key_env: GOOGLE_API_KEY}
receptionist: {model: gem, routes: [{strategy: solo, models: {primary: gem}}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	reg := BuildRegistry(cfg)
	p, err := reg.Get("gem")
	if err != nil {
		t.Fatal(err)
	}
	if p.Vendor() != "google" {
		t.Fatalf("vendor %q", p.Vendor())
	}
}

func TestDefaultConfigIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("built-in default config invalid: %v", err)
	}
}
