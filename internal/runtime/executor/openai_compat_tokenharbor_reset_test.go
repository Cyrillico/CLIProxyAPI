package executor

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

const tokenHarborFreeRejection = `{"error":{"message":"Free tier limit reached for this week. Your next rolling 7-day period starts on 11 Oct 2026 at 02:21 UTC.","type":"insufficient_quota"}}`

func TestTokenHarborFreeResetDelayParsesDeclaredWindow(t *testing.T) {
	now := time.Date(2026, 10, 5, 2, 21, 0, 0, time.UTC)
	delay := tokenHarborFreeResetDelay([]byte(tokenHarborFreeRejection), now)
	if delay == nil {
		t.Fatal("tokenHarborFreeResetDelay() = nil, want the declared window")
	}
	want := 6 * 24 * time.Hour
	if *delay != want {
		t.Fatalf("tokenHarborFreeResetDelay() = %s, want %s", *delay, want)
	}
}

func TestTokenHarborFreeResetDelayIgnoresUnrelatedBody(t *testing.T) {
	now := time.Date(2026, 10, 5, 2, 21, 0, 0, time.UTC)
	if delay := tokenHarborFreeResetDelay([]byte(`{"error":{"message":"rate limited"}}`), now); delay != nil {
		t.Fatalf("tokenHarborFreeResetDelay() = %s, want nil", *delay)
	}
}

func TestTokenHarborFreeResetDelayFloorsPastWindow(t *testing.T) {
	// A reset instant already in the past must never produce a zero cooldown.
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	delay := tokenHarborFreeResetDelay([]byte(tokenHarborFreeRejection), now)
	if delay == nil {
		t.Fatal("tokenHarborFreeResetDelay() = nil, want the floored window")
	}
	if *delay != minTokenHarborFreeCooldown {
		t.Fatalf("tokenHarborFreeResetDelay() = %s, want %s", *delay, minTokenHarborFreeCooldown)
	}
}

func TestOpenAICompatRetryAfterPrefersDeclaredResetOverHeader(t *testing.T) {
	now := time.Date(2026, 10, 4, 2, 21, 0, 0, time.UTC)
	headers := http.Header{"Retry-After": []string{"3600"}}
	delay := openAICompatRetryAfter(http.StatusTooManyRequests, headers, []byte(tokenHarborFreeRejection), now)
	if delay == nil {
		t.Fatal("openAICompatRetryAfter() = nil, want the declared reset")
	}
	if *delay == time.Hour {
		t.Fatalf("openAICompatRetryAfter() = %s, want the declared reset over Retry-After", *delay)
	}
	if want := time.Date(2026, 10, 11, 2, 21, 0, 0, time.UTC).Sub(now); *delay != want {
		t.Fatalf("openAICompatRetryAfter() = %s, want %s", *delay, want)
	}
}

func TestOpenAICompatRetryAfterKeepsHeaderWhenBodyNamesNoInstant(t *testing.T) {
	now := time.Date(2026, 10, 4, 2, 21, 0, 0, time.UTC)
	headers := http.Header{"Retry-After": []string{"3600"}}
	delay := openAICompatRetryAfter(http.StatusTooManyRequests, headers, []byte(`{"error":{"message":"slow down"}}`), now)
	if delay == nil || *delay != time.Hour {
		t.Fatalf("openAICompatRetryAfter() = %v, want 1h from the header", delay)
	}
}

func TestOpenAICompatStatusErrorIsCredentialScopedForFreeTierWindow(t *testing.T) {
	// The declared instant must be ahead of the wall clock, otherwise the
	// cooldown is floored and the "beyond the header hour" assertion is moot.
	future := fmt.Sprintf(`{"error":{"message":"Free tier limit reached. Your next rolling 7-day period starts on %s UTC.","type":"insufficient_quota"}}`,
		time.Now().UTC().Add(6*24*time.Hour).Format("2 Jan 2006 at 15:04"))
	errFree := newOpenAICompatStatusError(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"3600"}}, []byte(future))
	if !errFree.IsCredentialScoped() {
		t.Fatal("free-tier window rejection must be credential scoped")
	}
	if errFree.RetryAfter() == nil || *errFree.RetryAfter() <= time.Hour {
		t.Fatalf("free-tier window retryAfter = %v, want beyond the header hour", errFree.RetryAfter())
	}

	errPlain := newOpenAICompatStatusError(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"60"}}, []byte(`{"error":{"message":"slow down"}}`))
	if errPlain.IsCredentialScoped() {
		t.Fatal("a plain 429 must stay model scoped")
	}
}
