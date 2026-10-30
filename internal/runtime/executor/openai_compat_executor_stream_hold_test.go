package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatRetryBeforeCommit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "wrapped message", err: errors.New("read tcp: unexpected EOF"), want: true},
		{name: "failed event text", err: statusErr{code: http.StatusBadGateway, msg: `{"type":"response.failed","error":{"message":"unexpected EOF"}}`}, want: true},
		{name: "closed before done", err: statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before [DONE]"}, want: true},
		{name: "content failure", err: statusErr{code: http.StatusBadRequest, msg: `{"error":{"message":"flagged as high risk"}}`}, want: false},
		{name: "upstream failed", err: statusErr{code: http.StatusBadGateway, msg: `{"error":{"message":"upstream failed"}}`}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openAICompatRetryBeforeCommit(tc.err); got != tc.want {
				t.Fatalf("openAICompatRetryBeforeCommit() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestOpenAICompatExecutorHoldsTransportFailureForRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"error":{"message":"unexpected EOF"}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	request := []byte(`{"model":"deepseek-v4-flash","input":"hi","stream":true}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: request,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamed strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		streamed.Write(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if !strings.Contains(streamed.String(), "partial") && !strings.Contains(streamed.String(), "response.output_text.delta") {
		t.Fatalf("first token was held back: %q", streamed.String())
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "unexpected EOF") {
		t.Fatalf("stream error = %v, want unexpected EOF", streamErr)
	}
}

func TestOpenAICompatExecutorRetriesTransportFailureBeforeFirstToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"error":{"message":"unexpected EOF"}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	request := []byte(`{"model":"deepseek-v4-flash","input":"hi","stream":true}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: request,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var payloads int
	var streamErr error
	for chunk := range result.Chunks {
		if len(chunk.Payload) > 0 {
			payloads++
		}
		if chunk.Err != nil {
			if payloads > 0 {
				t.Fatalf("payload released before a pre-token transport error")
			}
			streamErr = chunk.Err
		}
	}
	if payloads != 0 {
		t.Fatalf("pre-token transport failure released %d payloads", payloads)
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "unexpected EOF") {
		t.Fatalf("stream error = %v, want unexpected EOF", streamErr)
	}
}

func TestOpenAICompatExecutorStillForwardsNonTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"error":{"type":"server_error","code":"upstream_failed","message":"upstream failed"}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	request := []byte(`{"model":"deepseek-v4-flash","input":"hi","stream":true}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: request,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamed strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		streamed.Write(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if !strings.Contains(streamed.String(), "response.output_text.delta") {
		t.Fatalf("non-transport failure dropped the partial output: %q", streamed.String())
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "upstream failed") {
		t.Fatalf("stream error = %v, want upstream failed", streamErr)
	}
}

func TestOpenAICompatExecutorReleasesHoldPastLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"partial-output-that-exceeds-the-tiny-hold"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"error":{"message":"unexpected EOF"}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	request := []byte(`{"model":"deepseek-v4-flash","input":"hi","stream":true}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: request,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamed strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		streamed.Write(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if !strings.Contains(streamed.String(), "partial-output-that-exceeds-the-tiny-hold") {
		t.Fatalf("oversized stream was not released: %q", streamed.String())
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "unexpected EOF") {
		t.Fatalf("stream error = %v, want unexpected EOF after release", streamErr)
	}
}
