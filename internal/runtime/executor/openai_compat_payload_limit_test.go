package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const tokenHarborLimitTestModel = "test-model"

func tokenHarborLimitTestPayload(size int) []byte {
	msg := strings.Repeat("a", size)
	return []byte(`{"model":"` + tokenHarborLimitTestModel + `","messages":[{"role":"user","content":"` + msg + `"}]}`)
}

func tokenHarborLimitTestUpstreamOK(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id":"chatcmpl-limit","object":"chat.completion","created":1,"model":"` + tokenHarborLimitTestModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
}

// tokenHarborLimitTestSetup wires a tokenharbor provider whose upstream is a
// counting httptest server, with the given max-request-bytes cap. It mirrors
// the production wiring: compat executors register under the
// "openai-compatible-<name>" scheduling key.
func tokenHarborLimitTestSetup(t *testing.T, limit int64) (*cliproxyauth.Manager, *cliproxyauth.Auth, *atomic.Int32, string) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		tokenHarborLimitTestUpstreamOK(w, r)
	}))
	t.Cleanup(server.Close)

	providerKey := util.OpenAICompatibleProviderKey("tokenharbor")
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:            "tokenharbor",
			BaseURL:         server.URL,
			MaxRequestBytes: limit,
		}},
	}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 2)
	manager.RegisterExecutor(NewOpenAICompatExecutor(providerKey, cfg))

	auth := &cliproxyauth.Auth{
		ID:       uuid.NewString(),
		Provider: "tokenharbor",
		Attributes: map[string]string{
			"api_key":     "k1",
			"base_url":    server.URL,
			"compat_name": "tokenharbor",
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, providerKey, []*registry.ModelInfo{{ID: tokenHarborLimitTestModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	auth.Status = cliproxyauth.StatusActive
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	return manager, auth, &hits, providerKey
}

func assertPayloadLimitRejects(t *testing.T, err error, hits *atomic.Int32, auth *cliproxyauth.Auth, manager *cliproxyauth.Manager) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a 413 rejection, got nil error")
	}
	if got := clienterror.HTTPStatusFromError(err); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", got)
	}
	msg := err.Error()
	for _, banned := range []string{"tokenharbor", "TokenHarbor", "free", "Free", "quota"} {
		if strings.Contains(msg, banned) {
			t.Fatalf("client message leaks provider/account wording: %q", msg)
		}
	}
	for _, want := range []string{"too large", "new session", "/compact"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("client message %q missing recovery guidance %q", msg, want)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream contacted %d times, want 0 (must reject before send)", hits.Load())
	}
	registered, ok := manager.GetByID(auth.ID)
	if !ok || registered == nil {
		t.Fatal("auth missing after rejection")
	}
	if registered.Unavailable {
		t.Fatal("a request-fault 413 must not cool down the credential")
	}
}

func TestOpenAICompatPayloadLimitRejectsBeforeUpstream_Execute(t *testing.T) {
	manager, auth, hits, providerKey := tokenHarborLimitTestSetup(t, 128)
	_, err := manager.Execute(context.Background(), []string{providerKey}, cliproxyexecutor.Request{
		Model:   tokenHarborLimitTestModel,
		Payload: tokenHarborLimitTestPayload(1024),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	assertPayloadLimitRejects(t, err, hits, auth, manager)
}

func TestOpenAICompatPayloadLimitRejectsBeforeUpstream_ExecuteStream(t *testing.T) {
	manager, auth, hits, providerKey := tokenHarborLimitTestSetup(t, 128)
	_, err := manager.ExecuteStream(context.Background(), []string{providerKey}, cliproxyexecutor.Request{
		Model:   tokenHarborLimitTestModel,
		Payload: tokenHarborLimitTestPayload(1024),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	assertPayloadLimitRejects(t, err, hits, auth, manager)
}

func TestOpenAICompatPayloadLimitPassesUnderLimit_Execute(t *testing.T) {
	manager, _, hits, providerKey := tokenHarborLimitTestSetup(t, 1<<20)
	resp, err := manager.Execute(context.Background(), []string{providerKey}, cliproxyexecutor.Request{
		Model:   tokenHarborLimitTestModel,
		Payload: tokenHarborLimitTestPayload(64),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("expected the under-limit request to pass through, got: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected an upstream response payload")
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hit %d times, want 1", hits.Load())
	}
}

func TestOpenAICompatPayloadLimitDisabledWhenZero_Execute(t *testing.T) {
	manager, _, hits, providerKey := tokenHarborLimitTestSetup(t, 0)
	if _, err := manager.Execute(context.Background(), []string{providerKey}, cliproxyexecutor.Request{
		Model:   tokenHarborLimitTestModel,
		Payload: tokenHarborLimitTestPayload(1024),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("expected the oversized request to pass when the cap is disabled, got: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hit %d times, want 1", hits.Load())
	}
}
