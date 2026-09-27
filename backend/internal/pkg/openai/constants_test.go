package openai

import "testing"

func TestDefaultModelsIncludeGPT6Astra(t *testing.T) {
	ids := DefaultModelIDs()
	for _, want := range []string{"gpt-6-astra", "gpt-6", "gpt-6-sol", "gpt-6-luna"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("DefaultModelIDs() missing %q: %v", want, ids)
		}
	}
}

// 账号测试与部分前端下拉取 DefaultModels 的首项作为默认模型，
// 新增 GPT-6 条目不得改变该首项。
func TestDefaultModelsPreferConcreteGPT56SolFirst(t *testing.T) {
	if len(DefaultModels) == 0 {
		t.Fatal("DefaultModels is empty")
	}
	if got := DefaultModels[0].ID; got != "gpt-5.6-sol" {
		t.Fatalf("DefaultModels[0].ID = %q, want gpt-5.6-sol", got)
	}
}

func TestDefaultModelsDisplayNames(t *testing.T) {
	want := map[string]string{
		"gpt-6-astra": "GPT-6 Astra",
		"gpt-6":       "GPT-6 (Astra)",
		"gpt-6-sol":   "GPT-6 Sol",
		"gpt-6-luna":  "GPT-6 Luna",
	}
	for _, m := range DefaultModels {
		if expected, ok := want[m.ID]; ok {
			if m.DisplayName != expected {
				t.Fatalf("model %q display name = %q, want %q", m.ID, m.DisplayName, expected)
			}
			delete(want, m.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("models missing from DefaultModels: %v", want)
	}
}

func TestIsGPT6SolOrLunaModelSpelling(t *testing.T) {
	for _, model := range []string{
		"gpt-6-sol", "gpt-6-luna", "openai/GPT-6_SOL", "gpt-6-sol-max",
		"gpt-6-luna-openai-compact", "gpt-6-sol-2026-09-23",
	} {
		if !IsGPT6SolOrLunaModelSpelling(model) {
			t.Fatalf("IsGPT6SolOrLunaModelSpelling(%q) = false", model)
		}
	}
	for _, model := range []string{"gpt-6-astra", "gpt-6-solar", "gpt-6-luna-preview"} {
		if IsGPT6SolOrLunaModelSpelling(model) {
			t.Fatalf("IsGPT6SolOrLunaModelSpelling(%q) = true", model)
		}
	}
}
