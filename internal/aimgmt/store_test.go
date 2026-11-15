package aimgmt

import (
	"strings"
	"testing"
	"tradebot/internal/profile"
)

func TestProviderPublishAndRollback(t *testing.T) {
	p, e := profile.Open(t.TempDir() + "/ai.db")
	if e != nil {
		t.Fatal(e)
	}
	defer p.DB.Close()
	s, e := New(p.DB, strings.Repeat("ab", 32))
	if e != nil {
		t.Fatal(e)
	}
	base := Version{Provider: "nvidia", Model: "nvidia/nemotron-3-ultra-550b-a55b", BaseURL: "https://integrate.api.nvidia.com/v1", TimeoutSeconds: 175, MaxTokens: 4096, Temperature: 1, ReasoningBudget: 1024, ReasoningEffort: "medium"}
	if e = s.Seed(base, "nvidia-secret"); e != nil {
		t.Fatal(e)
	}
	openai, e := s.Draft(profile.AdminID, Version{Provider: "openai", Model: "gpt-4.1-mini", MaxTokens: 1000, Temperature: 1, ReasoningEffort: "none"}, "openai-secret")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Publish(profile.AdminID, openai.ID); e != nil {
		t.Fatal(e)
	}
	c, e := s.Client(0)
	if e != nil || c.Provider != "openai" || c.Key != "openai-secret" || c.BaseURL != "https://api.openai.com/v1" || c.HTTP.Timeout.Seconds() != 150 {
		t.Fatal("provider switch failed")
	}
	versions, e := s.List(profile.AdminID)
	if e != nil || len(versions) != 2 {
		t.Fatal(e)
	}
	for _, v := range versions {
		if strings.Contains(v.Model, "secret") {
			t.Fatal("secret leaked")
		}
	}
	if e = s.Publish(profile.AdminID, 1); e != nil {
		t.Fatal(e)
	}
	c, e = s.Client(0)
	if e != nil || c.Provider != "nvidia" {
		t.Fatal("rollback failed")
	}
	if _, e = s.Draft(1, base, "bad"); e == nil {
		t.Fatal("unauthorized draft")
	}
	if e = s.SetEnabled(profile.AdminID, openai.ID, false); e != nil {
		t.Fatal(e)
	}
	versions, _ = s.List(profile.AdminID)
	if versions[0].Enabled {
		t.Fatal("provider disable was not persisted")
	}
}
