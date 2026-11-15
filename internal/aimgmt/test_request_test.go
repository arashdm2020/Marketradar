package aimgmt

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"tradebot/internal/profile"
)

type fakeTransport struct {
	calls   int
	keySeen bool
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	f.keySeen = r.Header.Get("Authorization") == "Bearer test-secret"
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)), Header: make(http.Header)}, nil
}
func TestBoundedAITestRequest(t *testing.T) {
	p, e := profile.Open(t.TempDir() + "/test.db")
	if e != nil {
		t.Fatal(e)
	}
	defer p.DB.Close()
	s, e := New(p.DB, strings.Repeat("bc", 32))
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.Draft(profile.AdminID, Version{Provider: "openai", Model: "gpt-4.1-mini", MaxTokens: 256, Temperature: 1, ReasoningEffort: "none"}, "test-secret")
	if e != nil {
		t.Fatal(e)
	}
	transport := &fakeTransport{}
	client := &http.Client{Transport: transport}
	for i := 0; i < 3; i++ {
		out, e := s.Test(context.Background(), profile.AdminID, v.ID, client)
		if e != nil || out.Status != "completed" || out.Sample != "{\"ok\":true}" || out.HTTPStatus != 200 || out.PromptTokens != 7 || out.CompletionTokens != 3 {
			t.Fatalf("test %d: %+v %v", i, out, e)
		}
	}
	if _, e := s.Test(context.Background(), profile.AdminID, v.ID, client); e == nil {
		t.Fatal("unbounded test requests")
	}
	if transport.calls != 3 || !transport.keySeen {
		t.Fatal("provider calls incorrect")
	}
}
