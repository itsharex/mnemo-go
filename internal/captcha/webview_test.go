package captcha

import (
	"strings"
	"testing"
	"time"
)

func TestReportTokenBeforeTokenlessRedirect(t *testing.T) {
	results := make(chan string, 2)
	session, err := Start(func(_ Session, token string) { results <- token })
	if err != nil {
		t.Fatal(err)
	}
	defer Close()
	queueCallback(session.ID, "")
	token := strings.Repeat("final-token", 4)
	if !acceptReport(session.ID, "https://user.mypikpak.com/credit/v1/report", []byte(`{"data":{"captcha_token":"`+token+`"}}`)) {
		t.Fatal("valid report was not accepted")
	}
	select {
	case got := <-results:
		if got != token {
			t.Fatalf("got %q, want final report token", got)
		}
	case <-time.After(time.Second):
		t.Fatal("completion not delivered")
	}
	select {
	case <-results:
		t.Fatal("duplicate completion")
	case <-time.After(650 * time.Millisecond):
	}
}

func TestReportRejectsUntrustedOrStaleResults(t *testing.T) {
	results := make(chan string, 2)
	session, err := Start(func(_ Session, token string) { results <- token })
	if err != nil {
		t.Fatal(err)
	}
	defer Close()
	body := []byte(`{"captcha_token":"` + strings.Repeat("x", 40) + `"}`)
	for _, raw := range []string{"https://mypikpak.com.evil.test/credit/v1/report", "http://user.mypikpak.com/credit/v1/report", "https://user.mypikpak.com/v1/captcha/init"} {
		if acceptReport(session.ID, raw, body) {
			t.Fatalf("accepted %s", raw)
		}
	}
	if acceptReport("old-session", "https://user.mypikpak.com/credit/v1/report", body) {
		t.Fatal("accepted stale report")
	}
	queueCallback(session.ID, "")
	if _, err := Start(func(_ Session, token string) { results <- token }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-results:
		t.Fatal("old callback completed replacement session")
	case <-time.After(650 * time.Millisecond):
	}
}

func TestCallbackURLTokenAndScope(t *testing.T) {
	s := Session{CallbackURL: "http://127.0.0.1:2345/callback/current"}
	token := strings.Repeat("t", 40)
	for _, raw := range []string{s.CallbackURL + "#captcha_token=" + token, "xlaccsdk01://xbase.cloud/callback?captcha_token=" + token} {
		got, ok := callbackToken(s, raw)
		if !ok || got != token {
			t.Fatalf("callback %s: %q %v", raw, got, ok)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:2345/callback/stale", "https://evil.test/callback", "xlaccsdk01://evil.test/callback"} {
		if _, ok := callbackToken(s, raw); ok {
			t.Fatalf("accepted unrelated callback %s", raw)
		}
	}
}
