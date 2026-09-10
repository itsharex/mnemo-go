package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/captcha"
	"mnemo-go/internal/drive"
	"mnemo-go/internal/drive/providers/pikpak"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

func TestPikPakListMock(t *testing.T) {
	// Mock pikpak drive API (api-drive.mypikpak.com)
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drive/v1/files" && r.Method == "GET" {
			json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{
					{"id": "1", "name": "video.mp4", "kind": "drive#file", "size": 1000, "parent_id": "root", "thumbnail_link": "", "modified_time": "2026-01-01T00:00:00Z", "web_content_link": ""},
					{"id": "2", "name": "folder", "kind": "drive#folder", "size": 0, "parent_id": "root", "modified_time": "2026-01-01T00:00:00Z", "web_content_link": ""},
				},
				"next_page_token": "",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "test-token", DeviceID: "dev", TokenFrom: "pikpak",
		RefreshToken: "test", ExpiresIn: 3600, UserID: "pikpak_test",
	})

	names := listNames(t, uid, did, "pikpak_root")
	if len(names) != 2 || names[0] != "video.mp4" || names[1] != "folder" {
		t.Fatalf("names: %v", names)
	}

	// Verify driver resolves correctly
	provider := drive.ProviderOf(uid, did, "")
	if provider != "pikpak" {
		t.Fatalf("provider: %s", provider)
	}
}

func TestPikPakApiCaptchaRetry(t *testing.T) {
	var initCalls, apiCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/shield/captcha/init":
			initCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"captcha_token": "api-captcha-1"})
		case "/drive/v1/files":
			apiCalls++
			if apiCalls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "captcha_required"})
				return
			}
			if got := r.Header.Get("X-Captcha-Token"); got != "api-captcha-1" {
				t.Fatalf("captcha header = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{"id": "1", "name": "ok.txt", "kind": "drive#file", "size": 1, "parent_id": "root"}}, "next_page_token": ""})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = pikpakCaptchaRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "api-access-token", DeviceID: "captcha-device", ProviderAccountID: "captcha-account",
		TokenFrom: "pikpak", UserID: "pikpak_captcha_test",
	})
	names := listNames(t, uid, did, "pikpak_root")
	if len(names) != 1 || names[0] != "ok.txt" {
		t.Fatalf("names: %v", names)
	}
	if initCalls != 1 || apiCalls != 2 {
		t.Fatalf("captcha flow calls: init=%d api=%d", initCalls, apiCalls)
	}
}

func TestPikPakLoginStopsWhenCaptchaInitFails(t *testing.T) {
	t.Cleanup(pikpak.ResetPikPakLoginCooldown)
	var signinCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/shield/captcha/init" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "too_many_requests"})
			return
		}
		if r.URL.Path == "/v1/auth/signin" {
			signinCalls++
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = pikpakCaptchaRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("pikpak")
	if !ok || reg.Auth == nil {
		t.Fatal("pikpak login registration missing")
	}
	_, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{
		"username": "login@example.com",
		"password": "password",
	}})
	var rateErr *pikpak.PikPakRateLimitError
	if err == nil || !errors.As(err, &rateErr) || rateErr.RetryAfterSeconds < 30 {
		t.Fatalf("login error = %v, want explicit cooldown", err)
	}
	if signinCalls != 0 {
		t.Fatalf("signin calls = %d, captcha init failure must stop login", signinCalls)
	}
}

func TestPikPakCaptchaCallbackCompletesCurrentSession(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{name: "with-final-token", token: "verified-captcha-token-123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer captcha.Close()
			completed := make(chan struct {
				session captcha.Session
				token   string
			}, 1)
			session, err := captcha.Start(func(got captcha.Session, token string) {
				completed <- struct {
					session captcha.Session
					token   string
				}{session: got, token: token}
			})
			if err != nil {
				t.Fatalf("start captcha callback: %v", err)
			}

			callbackURL := session.CallbackURL
			if tt.token != "" {
				callbackURL += "?captcha_token=" + tt.token
			}
			resp, err := http.Get(callbackURL)
			if err != nil {
				t.Fatalf("captcha callback request: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("captcha callback status = %d", resp.StatusCode)
			}

			select {
			case got := <-completed:
				if got.session.ID != session.ID || got.token != tt.token {
					t.Fatalf("captcha completion = %#v, want session=%q token=%q", got, session.ID, tt.token)
				}
			case <-time.After(time.Second):
				t.Fatal("captcha callback did not complete the session")
			}
		})
	}
}

func TestPikPakCaptchaFragmentCallback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the browser callback script")
	}
	for _, key := range []string{"captcha_token", "captchaToken", "token", ""} {
		t.Run("fragment-"+key, func(t *testing.T) {
			defer captcha.Close()
			done := make(chan string, 2)
			session, err := captcha.Start(func(_ captcha.Session, token string) { done <- token })
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			callbackURL := session.CallbackURL
			if key != "" {
				want = "verified-fragment-token-123456"
				callbackURL += "#" + key + "=" + want
			}
			// Real HTTP strips the fragment. The landing page must wait for its
			// browser script instead of completing with an empty token on GET.
			resp, err := http.Get(callbackURL)
			if err != nil {
				t.Fatal(err)
			}
			page, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			select {
			case token := <-done:
				t.Fatalf("callback completed before browser forwarded fragment: %q", token)
			case <-time.After(50 * time.Millisecond):
			}
			// Execute the actual landing-page JavaScript with browser URL state;
			// fetch still uses real HTTP against the callback server.
			script := `const vm = require('node:vm');
const page = process.argv[1], location = new URL(process.argv[2]);
const script = page.match(/<script>([\s\S]*?)<\/script>/)[1];
const document = {getElementById: () => ({textContent: ''})};
Promise.resolve(vm.runInNewContext(script, {
 location, URLSearchParams, document,
 history: {replaceState() {}},
 fetch: (path, opts) => fetch(new URL(path, location), opts),
 setTimeout() {}, window: {close() {}}
})).catch(err => {console.error(err); process.exitCode = 1});`
			if output, err := exec.Command(node, "-e", script, string(page), callbackURL).CombinedOutput(); err != nil {
				t.Fatalf("callback script: %v\n%s", err, output)
			}
			select {
			case got := <-done:
				if got != want {
					t.Fatalf("callback token = %q, want %q", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("browser callback did not complete")
			}
		})
	}
}

func TestPikPakCaptchaDelayedCloseKeepsNewSessionAlive(t *testing.T) {
	defer captcha.Close()
	oldCompleted := make(chan captcha.Session, 1)
	oldSession, err := captcha.Start(func(got captcha.Session, _ string) {
		oldCompleted <- got
	})
	if err != nil {
		t.Fatalf("start old captcha session: %v", err)
	}
	oldResponse, err := http.PostForm(oldSession.CallbackURL, url.Values{})
	if err != nil {
		t.Fatalf("complete old captcha session: %v", err)
	}
	_ = oldResponse.Body.Close()
	select {
	case got := <-oldCompleted:
		if got.ID != oldSession.ID {
			t.Fatalf("old session completion = %q, want %q", got.ID, oldSession.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("old captcha session did not complete")
	}

	newCompleted := make(chan captcha.Session, 1)
	newSession, err := captcha.Start(func(got captcha.Session, _ string) {
		newCompleted <- got
	})
	if err != nil {
		t.Fatalf("start new captcha session: %v", err)
	}
	// The first session schedules a delayed cleanup. It must compare session IDs
	// instead of closing the current session after a reload has created a new one.
	time.Sleep(2200 * time.Millisecond)
	newResponse, err := http.PostForm(newSession.CallbackURL, url.Values{})
	if err != nil {
		t.Fatalf("new captcha callback after delayed cleanup: %v", err)
	}
	_ = newResponse.Body.Close()
	select {
	case got := <-newCompleted:
		if got.ID != newSession.ID {
			t.Fatalf("new session completion = %q, want %q", got.ID, newSession.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("delayed cleanup closed the new captcha session")
	}
}

func TestPikPakLoginUsesConfiguredCaptchaRedirectURI(t *testing.T) {
	const callbackURI = "http://127.0.0.1:45678/callback/test-session"
	var gotRedirectURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/shield/captcha/init" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			RedirectURI string `json:"redirect_uri"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode captcha init: %v", err)
		}
		gotRedirectURI = body.RedirectURI
		_ = json.NewEncoder(w).Encode(map[string]any{
			"captcha_token": "initial-captcha-token",
			"url":           "https://captcha.example.test/slider",
		})
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = pikpakCaptchaRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("pikpak")
	if !ok || reg.Auth == nil {
		t.Fatal("pikpak login registration missing")
	}
	_, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{
		"username":             "login@example.com",
		"password":             "password",
		"captcha_redirect_uri": callbackURI,
	}})
	var challenge *pikpak.CaptchaRequiredError
	if !errors.As(err, &challenge) {
		t.Fatalf("login error = %v, want captcha challenge", err)
	}
	if gotRedirectURI != callbackURI {
		t.Fatalf("captcha redirect_uri = %q, want %q", gotRedirectURI, callbackURI)
	}
}

func TestPikPakLoginDoesNotChainVerifiedCaptcha(t *testing.T) {
	var initCalls, signinCalls int
	var previousTokens []string
	var redirectURIs []string
	const callbackURI = "http://127.0.0.1:45678/callback/retry-session"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Device-Id"); len(got) != 32 || strings.Trim(got, "0123456789abcdefABCDEF") != "" {
			t.Errorf("X-Device-Id = %q, want 32 hexadecimal characters", got)
		}
		if got := r.Header.Get("Referer"); got != "https://mypikpak.com/" {
			t.Errorf("Referer = %q", got)
		}
		if got := r.Header.Get("X-Client-Id"); got != "YUMx5nI8ZU8Ap8pm" {
			t.Errorf("X-Client-Id = %q", got)
		}
		switch r.URL.Path {
		case "/v1/shield/captcha/init":
			var body struct {
				CaptchaToken string `json:"captcha_token"`
				RedirectURI  string `json:"redirect_uri"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode captcha init: %v", err)
			}
			initCalls++
			previousTokens = append(previousTokens, body.CaptchaToken)
			redirectURIs = append(redirectURIs, body.RedirectURI)
			response := map[string]any{"captcha_token": "chain-" + string(rune('0'+initCalls))}
			if initCalls < 3 {
				response["url"] = "https://captcha.example.test/slider"
			}
			_ = json.NewEncoder(w).Encode(response)
		case "/v1/auth/signin":
			signinCalls++
			if got := r.Header.Get("X-Captcha-Token"); got != "verified-token" {
				t.Errorf("X-Captcha-Token = %q, want verified-token", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-login",
				"refresh_token": "refresh-login",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"sub":           "pik-login-account",
			})
		case "/drive/v1/about":
			_ = json.NewEncoder(w).Encode(map[string]any{"quota": map[string]any{"used": 1, "limit": 2}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = pikpakCaptchaRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("pikpak")
	if !ok || reg.Auth == nil {
		t.Fatal("pikpak login registration missing")
	}
	tok, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{
		"username":             "login@example.com",
		"password":             "password",
		"captcha_token":        "verified-token",
		"captcha_redirect_uri": callbackURI,
	}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if tok == nil || tok.AccessToken != "access-login" {
		t.Fatalf("token = %#v", tok)
	}
	if initCalls != 0 || signinCalls != 1 {
		t.Fatalf("captcha/signin calls = %d/%d, want 0/1", initCalls, signinCalls)
	}
	if len(previousTokens) != 0 || len(redirectURIs) != 0 {
		t.Fatalf("unexpected captcha init requests: previous=%v redirects=%v", previousTokens, redirectURIs)
	}
}

func TestPikPakLoginUsesVerifiedCaptchaTokenDirectly(t *testing.T) {
	var initCalls, signinCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/shield/captcha/init":
			initCalls++
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "captcha init must not run after visual verification"})
		case "/v1/auth/signin":
			signinCalls++
			if got := r.Header.Get("X-Captcha-Token"); got != "verified-final" {
				t.Errorf("X-Captcha-Token = %q, want verified-final", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "direct-access", "refresh_token": "direct-refresh",
				"expires_in": 3600, "token_type": "Bearer", "sub": "direct-account",
			})
		case "/drive/v1/about":
			_ = json.NewEncoder(w).Encode(map[string]any{"quota": map[string]any{"used": 0, "limit": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = pikpakCaptchaRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("pikpak")
	if !ok || reg.Auth == nil {
		t.Fatal("pikpak login registration missing")
	}
	tok, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{
		"username": "direct@example.com", "password": "password",
		"captcha_token": "verified-final", "captcha_verified": "true",
	}})
	if err != nil || tok == nil || tok.AccessToken != "direct-access" {
		t.Fatalf("login token/error = %#v/%v", tok, err)
	}
	if initCalls != 0 || signinCalls != 1 {
		t.Fatalf("captcha/signin calls = %d/%d, want 0/1", initCalls, signinCalls)
	}
}

func TestPikPakLoginRefreshesRejectedInitialCaptcha(t *testing.T) {
	var initCalls, signinCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Referer"); got != "https://mypikpak.com/" {
			t.Errorf("Referer = %q", got)
		}
		switch r.URL.Path {
		case "/v1/shield/captcha/init":
			var body struct {
				CaptchaToken string `json:"captcha_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode captcha init: %v", err)
			}
			initCalls++
			if initCalls == 1 && body.CaptchaToken != "" {
				t.Errorf("initial captcha previous token = %q", body.CaptchaToken)
			}
			if initCalls == 2 && body.CaptchaToken != "initial-token" {
				t.Errorf("retry captcha previous token = %q", body.CaptchaToken)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"captcha_token": map[int]string{1: "initial-token", 2: "final-token"}[initCalls]})
		case "/v1/auth/signin":
			signinCalls++
			if signinCalls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "captcha_required"})
				return
			}
			if got := r.Header.Get("X-Captcha-Token"); got != "final-token" {
				t.Errorf("retry X-Captcha-Token = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "initial-retry-access", "refresh_token": "initial-retry-refresh",
				"expires_in": 3600, "token_type": "Bearer", "sub": "initial-retry-account",
			})
		case "/drive/v1/about":
			_ = json.NewEncoder(w).Encode(map[string]any{"quota": map[string]any{"used": 0, "limit": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = pikpakCaptchaRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("pikpak")
	if !ok || reg.Auth == nil {
		t.Fatal("pikpak login registration missing")
	}
	tok, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{
		"username": "initial@example.com", "password": "password",
	}})
	if err != nil || tok == nil || tok.AccessToken != "initial-retry-access" {
		t.Fatalf("login token/error = %#v/%v", tok, err)
	}
	if initCalls != 2 || signinCalls != 2 {
		t.Fatalf("captcha/signin calls = %d/%d", initCalls, signinCalls)
	}
}

func TestPikPakLoginRefreshesInvalidCaptchaWithoutPreviousToken(t *testing.T) {
	var initCalls, signinCalls int
	var previousTokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/shield/captcha/init":
			var body struct {
				CaptchaToken string `json:"captcha_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode captcha init: %v", err)
			}
			initCalls++
			previousTokens = append(previousTokens, body.CaptchaToken)
			result := "initial-token"
			if initCalls == 2 {
				result = "final-token"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"captcha_token": result})
		case "/v1/auth/signin":
			signinCalls++
			if signinCalls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "captcha_invalid"})
				return
			}
			if got := r.Header.Get("X-Captcha-Token"); got != "final-token" {
				t.Errorf("retry X-Captcha-Token = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "invalid-retry-access", "refresh_token": "invalid-retry-refresh",
				"expires_in": 3600, "token_type": "Bearer", "sub": "invalid-retry-account",
			})
		case "/drive/v1/about":
			_ = json.NewEncoder(w).Encode(map[string]any{"quota": map[string]any{"used": 0, "limit": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	netx.TestTransportHook = pikpakCaptchaRewriteRT{mockHost: stripSchemeHost(srv.URL)}
	t.Cleanup(func() { netx.TestTransportHook = nil })

	reg, ok := drive.Get("pikpak")
	if !ok || reg.Auth == nil {
		t.Fatal("pikpak login registration missing")
	}
	tok, err := reg.Auth(context.Background(), drive.AuthRequest{Config: map[string]string{
		"username": "invalid@example.com", "password": "password",
	}})
	if err != nil || tok == nil || tok.AccessToken != "invalid-retry-access" {
		t.Fatalf("login token/error = %#v/%v", tok, err)
	}
	if initCalls != 2 || signinCalls != 2 {
		t.Fatalf("captcha/signin calls = %d/%d", initCalls, signinCalls)
	}
	if strings.Join(previousTokens, ",") != "," {
		t.Fatalf("previous tokens = %v, want no token reuse", previousTokens)
	}
}

func TestPikPakListUsesCompleteAndTrashFilters(t *testing.T) {
	var normalFilters, normalParents, normalLimits, normalPageSizes, normalTokens []string
	var trashQuery, trashLimit, trashPageSize string
	var normalCalls int
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive/v1/files" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("parent_id") == "*" {
			trashQuery = r.URL.Query().Get("filters") + "|parent=" + r.URL.Query().Get("parent_id")
			trashLimit = r.URL.Query().Get("limit")
			trashPageSize = r.URL.Query().Get("page_size")
		} else {
			normalCalls++
			normalFilters = append(normalFilters, r.URL.Query().Get("filters"))
			normalParents = append(normalParents, r.URL.Query().Get("parent_id"))
			normalLimits = append(normalLimits, r.URL.Query().Get("limit"))
			normalPageSizes = append(normalPageSizes, r.URL.Query().Get("page_size"))
			normalTokens = append(normalTokens, r.URL.Query().Get("page_token"))
		}
		next := ""
		if r.URL.Query().Get("parent_id") != "*" && normalCalls == 1 {
			next = "normal-next"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}, "next_page_token": next})
	}))
	_ = mock
	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "filter-token", DeviceID: "filter-device", ProviderAccountID: "filter-account",
		TokenFrom: "pikpak", UserID: "pikpak_filter_test",
	})
	if _, err := drive.ListDir(uid, did, "pikpak_root", nil); err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if _, err := drive.ListTrash(uid, did, nil); err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	if len(normalFilters) != 2 {
		t.Fatalf("normal request count = %d, want 2", len(normalFilters))
	}
	if !strings.Contains(normalFilters[0], `"phase":{"eq":"PHASE_TYPE_COMPLETE"}`) || !strings.Contains(normalFilters[0], `"trashed":{"eq":false}`) {
		t.Fatalf("normal filters = %s", normalFilters[0])
	}
	for i := range normalFilters {
		if normalLimits[i] != "100" || normalPageSizes[i] != "" {
			t.Fatalf("normal pagination query %d: limit=%q page_size=%q", i, normalLimits[i], normalPageSizes[i])
		}
		if normalParents[i] != "" {
			t.Fatalf("root list should not send parent_id: %q", normalParents[i])
		}
	}
	if normalTokens[0] != "" || normalTokens[1] != "normal-next" {
		t.Fatalf("normal page tokens = %v", normalTokens)
	}
	if !strings.Contains(trashQuery, `"trashed":{"eq":true}`) || !strings.Contains(trashQuery, "parent=*") {
		t.Fatalf("trash query = %s", trashQuery)
	}
	if trashLimit != "100" || trashPageSize != "" {
		t.Fatalf("trash pagination query: limit=%q page_size=%q", trashLimit, trashPageSize)
	}
}

func TestPikPakOfflineUsesLegacyPayloadAndPagination(t *testing.T) {
	var createBody map[string]any
	var listCalls int
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/files":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Fatalf("decode offline create: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"id": "offline-task-1"},
				"file": "offline-file-1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/drive/v1/tasks":
			listCalls++
			if r.URL.Query().Get("type") != "offline" || r.URL.Query().Get("limit") != "10000" ||
				r.URL.Query().Get("thumbnail_size") != "SIZE_SMALL" || r.URL.Query().Get("with") != "reference_resource" ||
				!strings.Contains(r.URL.Query().Get("filters"), `"phase":{"in":`) {
				t.Errorf("offline list query = %s", r.URL.RawQuery)
			}
			if listCalls == 1 {
				if got := r.URL.Query().Get("page_token"); got != "" {
					t.Errorf("first page token = %q", got)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"tasks": []map[string]any{{
						"id": "offline-task-1", "phase": "PHASE_TYPE_RUNNING", "progress": 0.2,
						"reference_resource": map[string]any{"id": "offline-file-1", "name": "movie.mp4", "size": "42"},
					}},
					"next_page_token": "offline-page-2",
				})
				return
			}
			if got := r.URL.Query().Get("page_token"); got != "offline-page-2" {
				t.Errorf("second page token = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tasks":           []map[string]any{{"id": "offline-task-2", "phase": "PHASE_TYPE_COMPLETE", "progress": 1}},
				"next_page_token": "",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "offline-token", DeviceID: "offline-device", ProviderAccountID: "offline-account",
		TokenFrom: "pikpak", UserID: "pikpak_offline_test",
	})
	ctx := drive.Context{UserID: uid, DriveID: did, TokenFrom: "pikpak", Token: &model.TokenInfo{
		AccessToken: "offline-token", DeviceID: "offline-device", ProviderAccountID: "offline-account", UserID: uid,
	}}
	d := drive.New("pikpak")
	creator, ok := d.(interface {
		OfflineCreate(context.Context, drive.Context, string, string, string) (string, string, error)
	})
	if !ok {
		t.Fatal("pikpak offline creator missing")
	}
	taskID, fileID, err := creator.OfflineCreate(context.Background(), ctx, "https://example.test/movie", "movie.mp4", "pikpak_root")
	if err != nil || taskID != "offline-task-1" || fileID != "offline-file-1" {
		t.Fatalf("offline create = %q/%q/%v", taskID, fileID, err)
	}
	if createBody["kind"] != "drive#file" || createBody["upload_type"] != "UPLOAD_TYPE_URL" || createBody["folder_type"] != "DOWNLOAD" {
		t.Fatalf("offline create body = %#v", createBody)
	}
	if _, ok := createBody["urls"]; ok {
		t.Fatalf("offline create must use url object: %#v", createBody)
	}
	urlBody, ok := createBody["url"].(map[string]any)
	if !ok || urlBody["url"] != "https://example.test/movie" {
		t.Fatalf("offline url body = %#v", createBody["url"])
	}
	lister, ok := d.(interface {
		OfflineList(context.Context, drive.Context) ([]pikpak.OfflineTask, error)
	})
	if !ok {
		t.Fatal("pikpak offline lister missing")
	}
	tasks, err := lister.OfflineList(context.Background(), ctx)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("offline list = %#v/%v", tasks, err)
	}
	if tasks[0].TaskID != "offline-task-1" || tasks[0].FileID != "offline-file-1" || tasks[0].Name != "movie.mp4" || tasks[0].Progress != 20 || tasks[0].FileSize != 42 {
		t.Fatalf("offline task mapping = %#v", tasks[0])
	}
}

func TestPikPakBatchAndShareUseLegacyRequests(t *testing.T) {
	var moveBody, copyBody, shareBody map[string]any
	var taskPolls int
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/files:batchMove":
			if err := json.NewDecoder(r.Body).Decode(&moveBody); err != nil {
				t.Fatalf("decode move: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []map[string]any{{"id": "move-task-1"}, {"id": "move-task-2"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/files:batchCopy":
			if err := json.NewDecoder(r.Body).Decode(&copyBody); err != nil {
				t.Fatalf("decode copy: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "copy-task-1"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/drive/v1/tasks/"):
			taskPolls++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/drive/v1/tasks/"), "phase": "PHASE_TYPE_COMPLETE"})
		case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/share":
			if err := json.NewDecoder(r.Body).Decode(&shareBody); err != nil {
				t.Fatalf("decode share: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"share_id": "share-1", "share_url": "https://mypikpak.com/s/share-1", "pass_code": "1234",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "batch-token", DeviceID: "batch-device", ProviderAccountID: "batch-account",
		TokenFrom: "pikpak", UserID: "pikpak_batch_test",
	})
	if _, err := drive.MoveBatch(uid, did, []drive.FileRef{{ID: "file-1"}, {ID: "file-2"}}, "folder-1", ""); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := drive.CopyBatch(uid, did, []drive.FileRef{{ID: "file-1"}}, "pikpak_root", ""); err != nil {
		t.Fatalf("copy: %v", err)
	}
	item, err := drive.CreateShare(uid, did, drive.ShareParams{
		FileIDs: []string{"file-1"}, ShareName: "movie", Password: "1234",
		Expiration: time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil || item == nil || item.ShareID != "share-1" {
		t.Fatalf("share = %#v/%v", item, err)
	}
	if taskPolls != 3 {
		t.Fatalf("task polls = %d, all batch tasks must be waited", taskPolls)
	}
	if got, ok := moveBody["ids"].([]any); !ok || len(got) != 2 {
		t.Fatalf("move ids = %#v", moveBody["ids"])
	}
	moveTo, ok := moveBody["to"].(map[string]any)
	if !ok || moveTo["parent_id"] != "folder-1" {
		t.Fatalf("move to = %#v", moveBody["to"])
	}
	copyTo, ok := copyBody["to"].(map[string]any)
	if !ok {
		t.Fatalf("copy to root = %#v", copyBody["to"])
	}
	if _, exists := copyTo["parent_id"]; exists {
		t.Fatalf("copy to root must omit parent_id = %#v", copyTo)
	}
	if shareBody["share_to"] != "encryptedlink" || shareBody["pass_code_option"] != "REQUIRED" {
		t.Fatalf("share body = %#v", shareBody)
	}
	if days, ok := shareBody["expiration_days"].(float64); !ok || days <= 0 {
		t.Fatalf("share expiration_days = %#v", shareBody["expiration_days"])
	}
}

func TestPikPakShareImportAcceptsShortAndQueryLinks(t *testing.T) {
	var shareCalls, detailCalls int
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/drive/v1/share":
			shareCalls++
			if got := r.URL.Query().Get("share_id"); got != "abc123" {
				t.Errorf("share id = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"share_status": "OK", "pass_code_token": "share-token", "file_id": "share-root",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/drive/v1/share/detail":
			detailCalls++
			q := r.URL.Query()
			if q.Get("share_id") != "abc123" || q.Get("pass_code_token") != "share-token" || q.Get("parent_id") != "share-root" {
				t.Errorf("share detail query = %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]any{{"id": "shared-file", "name": "movie.mp4", "kind": "drive#file", "size": 42}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "share-token", DeviceID: "share-device", ProviderAccountID: "share-account",
		TokenFrom: "pikpak", UserID: "pikpak_share_import_test",
	})
	ctx := drive.Context{UserID: uid, DriveID: did, TokenFrom: "pikpak", Token: &model.TokenInfo{
		AccessToken: "share-token", DeviceID: "share-device", ProviderAccountID: "share-account", UserID: uid,
	}}
	importer, ok := drive.New("pikpak").(interface {
		ImportShareSession(context.Context, drive.Context, string, string) (*drive.ShareImportSession, error)
	})
	if !ok {
		t.Fatal("pikpak share importer missing")
	}
	for _, link := range []string{
		"mypikpak.com/s/abc123?pass_code=2468",
		"https://mypikpak.com/?share_id=abc123&passcode=2468",
	} {
		session, err := importer.ImportShareSession(context.Background(), ctx, link, "")
		if err != nil || session == nil {
			t.Fatalf("import %q = %#v/%v", link, session, err)
		}
		if session.ShareID != "abc123" || session.Password != "2468" || session.PassCodeToken != "share-token" || len(session.Files) != 1 {
			t.Fatalf("import session %q = %#v", link, session)
		}
	}
	if shareCalls != 2 || detailCalls != 2 {
		t.Fatalf("share import calls: share=%d detail=%d", shareCalls, detailCalls)
	}
}

func TestPikPakVideoPreviewCachesAccountData(t *testing.T) {
	var detailCalls, playInfoCalls, vipCalls int
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/drive/v1/files/preview-1/video/play_info":
			playInfoCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"media_play_info": map[string]any{
				"transcode_list": []map[string]any{{
					"url": "https://cdn.example.test/video.ts?fid=preview-fid", "status": "success",
					"resolution_name": "1080P", "video": map[string]any{"video_type": "mpegts"},
				}},
			}})
		case "/drive/v1/files/preview-1":
			detailCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "preview-1", "name": "movie.mp4", "kind": "drive#file", "size": 2048,
				"links":  map[string]any{"application/octet-stream": map[string]any{"url": "https://cdn.example.test/origin.mp4?fid=preview-fid"}},
				"params": map[string]any{"duration": "120"},
				"medias": []map[string]any{{
					"is_origin": true, "category": "category_origin",
					"link":  map[string]any{"url": "https://cdn.example.test/origin-media.mp4?fid=preview-fid"},
					"video": map[string]any{"duration": "120000"},
				}},
			})
		case "/drive/v1/privilege/vip":
			vipCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"vip": map[string]any{"identity": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "preview-token", DeviceID: "preview-device", ProviderAccountID: "preview-account",
		TokenFrom: "pikpak", UserID: "pikpak_preview_test",
	})
	first, err := drive.GetVideoPreview(uid, did, "preview-1")
	if err != nil {
		t.Fatalf("first preview: %v", err)
	}
	second, err := drive.GetVideoPreview(uid, did, "preview-1")
	if err != nil {
		t.Fatalf("second preview: %v", err)
	}
	if first.Duration != 120 || second.Duration != 120 {
		t.Fatalf("duration = %d/%d", first.Duration, second.Duration)
	}
	if len(first.Qualities) < 2 || first.Qualities[0].Quality != "Origin" {
		t.Fatalf("qualities = %#v", first.Qualities)
	}
	var foundTS bool
	for _, quality := range first.Qualities {
		if quality.Type == "ts" {
			foundTS = quality.Height == 1080
		}
	}
	if !foundTS {
		t.Fatalf("qualities missing 1080p ts stream: %#v", first.Qualities)
	}
	if detailCalls != 1 || vipCalls != 1 || playInfoCalls != 2 {
		t.Fatalf("calls detail/vip/play = %d/%d/%d", detailCalls, vipCalls, playInfoCalls)
	}
}

func TestPikPakDownloadRefreshesExpiringDetail(t *testing.T) {
	detailCalls := 0
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive/v1/files/expiring-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		detailCalls++
		expireAt := time.Now().Add(10 * time.Second).Unix()
		if detailCalls > 1 {
			expireAt = time.Now().Add(3600 * time.Second).Unix()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "expiring-1", "name": "movie.mp4", "kind": "drive#file", "size": 1,
			"web_content_link": "https://cdn.example.test/movie.mp4?expire=" + strconv.FormatInt(expireAt, 10),
		})
	}))
	_ = mock
	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "expiry-token", DeviceID: "expiry-device", ProviderAccountID: "expiry-account",
		TokenFrom: "pikpak", UserID: "pikpak_expiry_test",
	})
	if _, err := drive.GetDownloadURL(uid, did, "expiring-1", 0); err != nil {
		t.Fatalf("first download url: %v", err)
	}
	if _, err := drive.GetDownloadURL(uid, did, "expiring-1", 0); err != nil {
		t.Fatalf("second download url: %v", err)
	}
	if detailCalls != 2 {
		t.Fatalf("detail calls = %d, expiring cache entry should refresh", detailCalls)
	}
}

func TestPikPakUploadHonorsConflictPolicy(t *testing.T) {
	filePath := t.TempDir() + "/same.bin"
	if err := os.WriteFile(filePath, []byte("pikpak-upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	var listCalls, trashCalls, createCalls int
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/drive/v1/files":
			listCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{
				"id": "old-file", "name": "same.bin", "kind": "drive#file", "phase": "PHASE_TYPE_COMPLETE",
			}}, "next_page_token": ""})
		case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/files:batchTrash":
			trashCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/files":
			createCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "new-file"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	_ = mock
	ctx := drive.Context{UserID: "pikpak_upload_test", DriveID: "pikpak_upload_drive", TokenFrom: "pikpak", Token: &model.TokenInfo{
		AccessToken: "upload-token", DeviceID: "upload-device", ProviderAccountID: "upload-account", UserID: "pikpak_upload_test",
	}}
	driver := drive.New("pikpak")

	refuse := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: filePath, ParentFileID: "pikpak_root", Name: "same.bin", ConflictPolicy: "refuse",
	}}
	if err := driver.UploadOneFile(context.Background(), ctx, refuse); err == nil || !strings.Contains(err.Error(), "同名") {
		t.Fatalf("refuse error = %v", err)
	}
	if createCalls != 0 || trashCalls != 0 {
		t.Fatalf("refuse create/trash = %d/%d", createCalls, trashCalls)
	}

	skip := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: filePath, ParentFileID: "pikpak_root", Name: "same.bin", ConflictPolicy: "skip",
	}}
	if err := driver.UploadOneFile(context.Background(), ctx, skip); err != nil {
		t.Fatalf("skip: %v", err)
	}
	if createCalls != 0 || trashCalls != 0 {
		t.Fatalf("skip create/trash = %d/%d", createCalls, trashCalls)
	}

	rename := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: filePath, ParentFileID: "pikpak_root", Name: "same.bin", ConflictPolicy: "rename",
	}}
	if err := driver.UploadOneFile(context.Background(), ctx, rename); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if rename.Info.Name != "same (1).bin" || rename.Upload.FileID != "new-file" {
		t.Fatalf("rename state/name = %q/%#v", rename.Info.Name, rename.Upload)
	}

	overwrite := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: filePath, ParentFileID: "pikpak_root", Name: "same.bin", ConflictPolicy: "overwrite",
	}}
	if err := driver.UploadOneFile(context.Background(), ctx, overwrite); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if listCalls != 5 || trashCalls != 1 || createCalls != 2 {
		t.Fatalf("list/trash/create = %d/%d/%d", listCalls, trashCalls, createCalls)
	}
	if overwrite.Upload.FileID != "new-file" || overwrite.Upload.DownSize != int64(len("pikpak-upload")) || overwrite.Upload.DownProcess != 100 || !overwrite.Upload.IsCompleted {
		t.Fatalf("upload state = %#v", overwrite.Upload)
	}
}

func TestPikPakOSSUploadProgressAndCleanup(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cleanup-on-failure"}[failed], func(t *testing.T) {
			filePath := t.TempDir() + "/oss.bin"
			content := []byte(strings.Repeat("x", 128*1024))
			if err := os.WriteFile(filePath, content, 0o600); err != nil {
				t.Fatal(err)
			}
			var putCalls, cleanupCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/drive/v1/files":
					_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}, "next_page_token": ""})
				case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/files":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "oss-file", "resumable": map[string]any{"params": map[string]any{
						"access_key_id": "ak", "access_key_secret": "sk", "bucket": "bucket", "endpoint": "oss.example.test", "key": "upload/oss.bin", "security_token": "st",
					}}})
				case r.Method == http.MethodPut && r.URL.Path == "/upload/oss.bin":
					putCalls++
					_, _ = io.Copy(io.Discard, r.Body)
					if failed {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = io.WriteString(w, "oss failed")
						return
					}
					w.WriteHeader(http.StatusOK)
				case r.Method == http.MethodPost && r.URL.Path == "/drive/v1/files:batchDelete":
					cleanupCalls++
					_ = json.NewEncoder(w).Encode(map[string]any{})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)
			netx.TestTransportHook = pikpakUploadRewriteRT{mockHost: stripSchemeHost(srv.URL)}
			t.Cleanup(func() { netx.TestTransportHook = nil })
			ui := &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: filePath, ParentFileID: "pikpak_root", Name: "oss.bin"}}
			ctx := drive.Context{UserID: "pikpak_oss_test", DriveID: "pikpak_oss_drive", TokenFrom: "pikpak", Token: &model.TokenInfo{
				AccessToken: "oss-token", DeviceID: "oss-device", ProviderAccountID: "oss-account", UserID: "pikpak_oss_test",
			}}
			err := drive.New("pikpak").UploadOneFile(context.Background(), ctx, ui)
			if failed {
				if err == nil || cleanupCalls != 1 {
					t.Fatalf("error/cleanup = %v/%d", err, cleanupCalls)
				}
				return
			}
			if err != nil || putCalls != 1 || cleanupCalls != 0 {
				t.Fatalf("success err/put/cleanup = %v/%d/%d", err, putCalls, cleanupCalls)
			}
			if ui.Upload.DownSize != int64(len(content)) || ui.Upload.DownProcess != 100 || !ui.Upload.IsCompleted {
				t.Fatalf("progress = %#v", ui.Upload)
			}
		})
	}
}

type pikpakCaptchaRewriteRT struct{ mockHost string }

func (r pikpakCaptchaRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != "api-drive.mypikpak.com" && req.URL.Hostname() != "user.mypikpak.com" {
		return http.DefaultTransport.RoundTrip(req)
	}
	u := *req.URL
	u.Scheme = "http"
	u.Host = r.mockHost
	req2 := req.Clone(req.Context())
	req2.URL = &u
	req2.RequestURI = ""
	return http.DefaultTransport.RoundTrip(req2)
}

type pikpakUploadRewriteRT struct{ mockHost string }

func (r pikpakUploadRewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() != "api-drive.mypikpak.com" && !strings.HasSuffix(req.URL.Hostname(), ".oss.example.test") {
		return http.DefaultTransport.RoundTrip(req)
	}
	u := *req.URL
	u.Scheme = "http"
	u.Host = r.mockHost
	req2 := req.Clone(req.Context())
	req2.URL = &u
	req2.RequestURI = ""
	return http.DefaultTransport.RoundTrip(req2)
}

func TestPikPakDownloadURL(t *testing.T) {
	mock := MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drive/v1/files/1/download" {
			json.NewEncoder(w).Encode(map[string]any{"url": "https://dl.example.com/file", "size": 1000})
			return
		}
		if r.URL.Path == "/drive/v1/files/1" {
			json.NewEncoder(w).Encode(map[string]any{"id": "1", "name": "v.mp4", "kind": "drive#file", "size": 1000, "parent_id": "root", "web_content_link": "https://dl.example.com/file", "modified_time": time.Now().Format(time.RFC3339)})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "pikpak", &model.TokenInfo{
		AccessToken: "test-token", TokenFrom: "pikpak", UserID: "pikpak_test",
	})

	dl, err := drive.GetDownloadURL(uid, did, "1", 3600)
	if err != nil {
		t.Fatalf("GetDownloadURL: %v", err)
	}
	if dl.URL == "" {
		t.Fatal("empty download URL")
	}
	if dl.Size != 1000 {
		t.Fatalf("size: %d", dl.Size)
	}
}

func TestPikPakDownloadPreservesFileDetailFailure(t *testing.T) {
	var fallbackCalls int
	MockAPI(t, "api-drive.mypikpak.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/drive/v1/files/permission-file":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"permission_denied","error_description":"file access is forbidden"}`)
		case "/drive/v1/files/permission-file/download":
			fallbackCalls++
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx := drive.Context{UserID: "pikpak_permission_test", TokenFrom: "pikpak", Token: &model.TokenInfo{
		AccessToken: "test-token", DeviceID: "test-device", ProviderAccountID: "test-account",
	}}
	_, err := drive.New("pikpak").GetDownloadURL(context.Background(), ctx, "permission-file", 3600)
	if err == nil || !strings.Contains(err.Error(), "forbidden") || fallbackCalls != 0 {
		t.Fatalf("detail permission error must not become fallback not_found: err=%v fallbackCalls=%d", err, fallbackCalls)
	}
}

func TestOneDriveListMock(t *testing.T) {
	mock := MockAPI(t, "graph.microsoft.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0/me/drive/root/children" {
			json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": "1", "name": "doc.txt", "size": 500, "file": map[string]any{"mimeType": "text/plain"}, "lastModifiedDateTime": "2026-01-01T00:00:00Z", "parentReference": map[string]any{"id": "root"}},
					{"id": "2", "name": "pics", "folder": map[string]any{}, "lastModifiedDateTime": "2026-01-01T00:00:00Z", "parentReference": map[string]any{"id": "root"}},
				},
				"@odata.nextLink": nil,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "onedrive", &model.TokenInfo{
		AccessToken: "test-token", TokenFrom: "onedrive", UserID: "onedrive_test",
	})

	names := listNames(t, uid, did, "onedrive_root")
	if len(names) != 2 || names[0] != "doc.txt" || names[1] != "pics" {
		t.Fatalf("names: %v", names)
	}
}

func TestOneDriveDownloadURL(t *testing.T) {
	mock := MockAPI(t, "graph.microsoft.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0/me/drive/items/1" {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "1", "name": "doc.txt", "size": 500, "file": map[string]any{"mimeType": "text/plain"},
				"@microsoft.graph.downloadUrl": "https://dl.example.com/doc",
				"lastModifiedDateTime":         "2026-01-01T00:00:00Z", "parentReference": map[string]any{"id": "root"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "onedrive", &model.TokenInfo{
		AccessToken: "test-token", TokenFrom: "onedrive", UserID: "onedrive_test",
	})

	dl, err := drive.GetDownloadURL(uid, did, "1", 3600)
	if err != nil {
		t.Fatalf("GetDownloadURL: %v", err)
	}
	if dl.URL == "" {
		t.Fatal("empty download URL")
	}
}

func TestDropboxExpiredTokenIsRefreshedAndPersisted(t *testing.T) {
	refreshes := 0
	MockAPI(t, "api.dropboxapi.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			refreshes++
			_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":14400}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-access" {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error_summary":"expired_access_token/"}`)
			return
		}
		_, _ = io.WriteString(w, `{"entries":[{".tag":"file","id":"id:one","name":"one.txt"}],"has_more":false}`)
	}))
	uid, did, st := SeedAccount(t, "dropbox", &model.TokenInfo{TokenFrom: "dropbox", AccessToken: "old-access", RefreshToken: "old-refresh"})
	drive.SetTokenUpdater(func(userID, driveID string, token *model.TokenInfo) error {
		account, err := st.GetAccount(userID)
		if err != nil {
			return err
		}
		account.Token = token
		return st.SaveAccount(account)
	})
	t.Cleanup(func() { drive.SetTokenUpdater(nil) })
	for i := 0; i < 2; i++ {
		files, err := drive.ListDir(uid, did, "dropbox_root", nil)
		if err != nil || len(files) != 1 {
			t.Fatalf("listing %d: %v, %v", i, files, err)
		}
	}
	account, err := st.GetAccount(uid)
	if err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 || account.Token.AccessToken != "new-access" || account.Token.RefreshToken != "new-refresh" || account.Token.ExpireTime == "" {
		t.Fatal("next operation did not reuse persisted refreshed token")
	}
}

func TestDropboxListMock(t *testing.T) {
	mock := MockAPI(t, "api.dropboxapi.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2/files/list_folder" {
			json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{".tag": "file", "id": "id:1", "name": "note.txt", "path_display": "/note.txt", "size": 300, "server_modified": "2026-01-01T00:00:00Z"},
					{".tag": "folder", "id": "id:2", "name": "data", "path_display": "/data", "size": 0, "server_modified": "2026-01-01T00:00:00Z"},
				},
				"cursor": "", "has_more": false,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "dropbox", &model.TokenInfo{
		AccessToken: "test-token", TokenFrom: "dropbox", UserID: "dropbox_test",
	})

	names := listNames(t, uid, did, "dropbox_root")
	if len(names) != 2 || names[0] != "note.txt" || names[1] != "data" {
		t.Fatalf("names: %v", names)
	}
}

func TestAliopenListMock(t *testing.T) {
	session := map[string]any{
		"access_token": "test-token", "refresh_token": "test-refresh",
		"drive_id": "d1", "resource_drive_id": "d2",
	}
	sessionJSON, _ := json.Marshal(session)
	mock := MockAPI(t, "openapi.alipan.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/adrive/v1.0/openFile/list":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"file_id": "f1", "name": "report.pdf", "parent_file_id": "root", "type": "file", "size": 2000, "updated_at": "2026-01-01T00:00:00Z", "content_hash": "", "thumbnail": "", "category": "doc"},
					{"file_id": "f2", "name": "images", "parent_file_id": "root", "type": "folder", "size": 0, "updated_at": "2026-01-01T00:00:00Z", "content_hash": "", "thumbnail": ""},
				},
				"next_marker": "",
			})
		default:
			// For getDriveInfo during boot
			json.NewEncoder(w).Encode(map[string]any{"default_drive_id": "d1", "resource_drive_id": "d2"})
		}
	}))
	_ = mock

	uid, did, _ := SeedAccount(t, "aliopen", &model.TokenInfo{
		AccessToken: "test-token", RefreshToken: string(sessionJSON),
		TokenFrom: "aliopen", UserID: "aliopen_test",
	})

	// list on backup root (will show virtual backup/resource dirs first)
	names := listNames(t, uid, did, "aliopen_root")
	_ = names
	// first call returns virtual dirs, not the API list
	if len(names) != 2 || names[0] != "备份盘" {
		t.Fatalf("root names: %v", names)
	}
}
