package config

import "testing"

func TestLoadVisionDefaultsAndTextSideChannels(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_MODEL", "deepseek-v4-flash-vision-exp")
	t.Setenv("LLM_MULTIMODAL", "")
	t.Setenv("COMPACTION_MODEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LLM.Multimodal {
		t.Fatal("vision model should enable multimodal by default")
	}
	if cfg.LLM.MaxTokens != 32000 {
		t.Fatalf("max output tokens = %d", cfg.LLM.MaxTokens)
	}
	if cfg.Compaction.Model != "deepseek-v4-flash" {
		t.Fatalf("compaction model = %q", cfg.Compaction.Model)
	}
	if cfg.Compaction.WindowNominalTokens != 1000000 ||
		cfg.Compaction.WindowUsableRatio != 0.90 ||
		cfg.Compaction.ReservedOutputTokens != 32000 ||
		cfg.Compaction.BufferTokens != 20000 {
		t.Fatalf("unexpected compaction defaults: %+v", cfg.Compaction)
	}
}

func TestLoadAllowsExplicitMultimodalOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "deepseek-v4-flash-vision-exp")
	t.Setenv("LLM_MULTIMODAL", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.Multimodal {
		t.Fatal("explicit LLM_MULTIMODAL=false was ignored")
	}
}

func TestIsVisionModel(t *testing.T) {
	for _, model := range []string{
		"deepseek-v4-flash-vision-exp",
		"custom-VISION-model",
	} {
		if !isVisionModel(model) {
			t.Errorf("isVisionModel(%q) = false", model)
		}
	}
	if isVisionModel("deepseek-v4-flash") {
		t.Error("text model detected as vision")
	}
}
