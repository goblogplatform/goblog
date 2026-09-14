package auth

import (
	"testing"
	"time"
)

// TestIPLimiter_Allow exercises ipLimiter.allow directly (an internal test,
// since ipLimiter is unexported) so the sliding window can be tested with
// injected times instead of real sleeps. Handler-level behaviour (the 429
// response, distinct addresses, a different IP) is covered by
// TestSendLoginCode_PerIPRateLimit in otp_test.go.
func TestIPLimiter_Allow(t *testing.T) {
	l := newIPLimiter()
	base := time.Now()

	for i := 0; i < loginCodeSendsPerIP; i++ {
		if !l.allow("1.2.3.4", base) {
			t.Fatalf("send %d should be allowed within the limit", i)
		}
	}
	if l.allow("1.2.3.4", base) {
		t.Fatal("expected the request over the limit to be rejected")
	}

	// A different key has its own budget.
	if !l.allow("5.6.7.8", base) {
		t.Fatal("expected a different key to be unaffected")
	}

	// After the window elapses, the original key is allowed again.
	after := base.Add(loginCodeSendsWindow + time.Second)
	if !l.allow("1.2.3.4", after) {
		t.Fatal("expected the limiter to allow again once the window has passed")
	}
}

// TestParseGitHubUser covers F4: a non-JSON body and a body with no id must
// be rejected rather than silently producing ProviderID "0".
func TestParseGitHubUser(t *testing.T) {
	user, err := parseGitHubUser([]byte(`{"id":42,"login":"octocat","avatar_url":"a","name":"n","email":"e@example.com"}`), "tok")
	if err != nil {
		t.Fatalf("valid body: unexpected error: %v", err)
	}
	if user.ProviderID != "42" || user.Login != "octocat" || user.AccessToken != "tok" {
		t.Fatalf("unexpected user: %+v", user)
	}

	if _, err := parseGitHubUser([]byte("not json"), "tok"); err == nil {
		t.Fatal("expected an error for a non-JSON body")
	}

	if _, err := parseGitHubUser([]byte(`{"login":"x"}`), "tok"); err == nil {
		t.Fatal("expected an error for a body with no id")
	}
}
