package config

import "testing"

func TestRerankTemplatesValidationAndHotReload(t *testing.T) {
	initial := validConfig()
	initial.RerankTemplates = map[string]RerankTemplate{"look": {Instruct: "match all", QueryTemplate: "检索{{query}}"}}
	live := NewLiveConfig(initial)
	next := validConfig()
	next.RerankTemplates = map[string]RerankTemplate{"look": {Instruct: "updated", QueryTemplate: "新模板{{query}}"}}
	if fields := RestartRequiredFields(initial, next); len(fields) != 0 {
		t.Fatalf("templates require restart: %v", fields)
	}
	live.ApplyYAML(mustYAML(t, next))
	if got := live.Load().RerankTemplates["look"]; got != next.RerankTemplates["look"] {
		t.Fatalf("hot reload=%+v", got)
	}
	for _, invalid := range []RerankTemplate{
		{Instruct: "", QueryTemplate: "{{query}}"},
		{Instruct: "match", QueryTemplate: "missing query"},
		{Instruct: "match", QueryTemplate: "{{query}} {{other}}"},
	} {
		next.RerankTemplates["look"] = invalid
		if err := next.Validate(); err == nil {
			t.Fatalf("accepted invalid template: %+v", invalid)
		}
		live.ApplyYAML(mustYAML(t, next))
		if live.Load().RerankTemplates["look"].Instruct != "updated" {
			t.Fatal("invalid update replaced current config")
		}
	}
	for _, key := range []string{"", " look "} {
		next.RerankTemplates = map[string]RerankTemplate{key: {Instruct: "match", QueryTemplate: "{{query}}"}}
		if err := next.Validate(); err == nil {
			t.Fatalf("accepted key %q", key)
		}
	}
}
