package pikpak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

func TestFileSizeAcceptsQuotedAndNumericIntegers(t *testing.T) {
	for _, raw := range []string{`"9007199254740993"`, `9007199254740993`, `"0"`, `0`, `null`} {
		t.Run(raw, func(t *testing.T) {
			var page listResp
			if err := json.Unmarshal([]byte(`{"files":[{"id":"file-1","name":"test.bin","size":`+raw+`}],"next_page_token":"next"}`), &page); err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			if strings.Contains(raw, "9007199254740993") {
				want = 9007199254740993
			}
			if len(page.Files) != 1 || page.Files[0].Size != want || page.Files[0].ID != "file-1" || page.NextPageToken != "next" {
				t.Fatalf("decoded page: %+v", page)
			}
		})
	}
	for _, raw := range []string{`"invalid"`, `"1.5"`, `1.5`, `true`, `"9223372036854775808"`} {
		var file File
		if err := json.Unmarshal([]byte(`{"size":`+raw+`}`), &file); err == nil {
			t.Errorf("accepted invalid size %s", raw)
		}
	}
}

func TestFavoritePaginationAndNativeStarOperations(t *testing.T) {
	c := newClient("token", "device", "favorite-test")
	reads, writes := 0, 0
	c.http.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			reads++
			q := r.URL.Query()
			var filter map[string]map[string]any
			if err := json.Unmarshal([]byte(q.Get("filters")), &filter); err != nil {
				t.Fatal(err)
			}
			if r.URL.Path != "/drive/v1/files" || q.Get("parent_id") != "*" || filter["starred"]["eq"] != true || filter["trashed"]["eq"] != false {
				t.Fatalf("wrong favorite filter: %s", r.URL)
			}
			if reads == 1 {
				return pikpakResponse(r, 200, `{"files":[{"id":"a","parent_id":"folder","name":"a.jpg","starred":true}],"next_page_token":"next"}`), nil
			}
			if q.Get("page_token") != "next" {
				t.Fatal("page token lost")
			}
			return pikpakResponse(r, 200, `{"files":[{"id":"b","name":"b.mp4","starred":true}],"next_page_token":""}`), nil
		}
		writes++
		want := "/drive/v1/files:star"
		if writes == 2 {
			want = "/drive/v1/files:unstar"
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != want || len(body.IDs) != 1 || body.IDs[0] != "a" {
			t.Fatalf("wrong star request: %s %+v", r.URL, body)
		}
		return pikpakResponse(r, 200, `{}`), nil
	})
	files, err := c.ListFavorites(context.Background())
	if err != nil || len(files) != 2 || files[0].ParentID != "folder" {
		t.Fatalf("favorites=%+v, %v", files, err)
	}
	for _, starred := range []bool{true, false} {
		if err := c.Star(context.Background(), []string{"a"}, starred); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFavoriteMalformedAndCyclicPagesAreNotEmptySuccess(t *testing.T) {
	for _, body := range []string{`{}`, `{"files":null}`, `{"files":[],"next_page_token":"cycle"}`} {
		t.Run(body, func(t *testing.T) {
			c := newClient("token", "device", "favorite-invalid")
			c.http.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) { return pikpakResponse(r, 200, body), nil })
			if _, err := c.ListFavorites(context.Background()); err == nil {
				t.Fatal("invalid favorite response accepted")
			}
		})
	}
}

func TestPikPakQuotedQuotaAndDownloadSize(t *testing.T) {
	c := newClient("token", "device", "account")
	c.http.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/drive/v1/about":
			return pikpakResponse(r, 200, `{"quota":{"used":"25","limit":"100"}}`), nil
		case "/drive/v1/files/file-1":
			return pikpakResponse(r, 200, `{"id":"file-1","size":"42"}`), nil
		case "/drive/v1/files/file-1/download":
			return pikpakResponse(r, 200, `{"url":"https://download.example/file","size":"42"}`), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})
	if used, total := c.About(context.Background()); used != 25 || total != 100 {
		t.Fatalf("quota: used=%d total=%d", used, total)
	}
	if link, size, err := c.DownloadURL(context.Background(), "file-1"); err != nil || size != 42 || link != "https://download.example/file" {
		t.Fatalf("download: link=%q size=%d err=%v", link, size, err)
	}
}

func TestPikPakUsageAndFolderThumbnail(t *testing.T) {
	c := newClient("token", "device", "account")
	c.http.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) {
		return pikpakResponse(r, 200, `{"quota":{"usage":"42","limit":"100"}}`), nil
	})
	if used, total := c.About(context.Background()); used != 42 || total != 100 {
		t.Fatalf("quota usage=%d limit=%d", used, total)
	}
	folder := mapFile(&File{ID: "folder", Name: "Videos", Kind: "drive#folder", Thumbnail: "https://example.test/video.jpg"}, "drive", "root")
	if folder.Thumbnail != "" {
		t.Fatal("folder uses its child's video thumbnail")
	}
	file := mapFile(&File{ID: "video", Name: "video.mp4", Thumbnail: "https://example.test/video.jpg"}, "drive", "root")
	if file.Thumbnail == "" {
		t.Fatal("video thumbnail removed")
	}
}

func TestPikPakPreviewUsesDetailMedias(t *testing.T) {
	c := newClient("token", "device", "preview-medias-account")
	c.http.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/drive/v1/files/video":
			if r.URL.Query().Get("usage") != "CACHE" || r.URL.Query().Get("_magic") != "2021" {
				return pikpakResponse(r, 200, `{"id":"video","size":"42"}`), nil
			}
			return pikpakResponse(r, 200, `{"id":"video","size":"42","medias":[{"media_name":"720p","is_visible":true,"link":{"url":"https://example.test/video.m3u8"},"video":{"height":720,"width":1280}},{"is_visible":false,"link":{"url":"https://example.test/hidden.m3u8"}}]}`), nil
		case "/drive/v1/privilege/vip":
			return pikpakResponse(r, 200, `{"vip":{"identity":0}}`), nil
		default:
			return pikpakResponse(r, 404, `{"error":"not_found"}`), nil
		}
	})
	preview, err := c.PlayInfo(context.Background(), "video")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Size != 42 || len(preview.Qualities) != 1 || preview.Qualities[0].URL != "https://example.test/video.m3u8" {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestPikPakVIPResponseUsesDataEnvelope(t *testing.T) {
	for _, tc := range []struct {
		status, kind string
		want         bool
	}{{"ok", "platinum", true}, {"invalid", "platinum", false}, {"ok", "novip", false}} {
		t.Run(tc.status+tc.kind, func(t *testing.T) {
			c := newClient("token", "vip-schema", tc.status+tc.kind)
			c.http.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) {
				return pikpakResponse(r, 200, fmt.Sprintf(`{"data":{"status":%q,"type":%q}}`, tc.status, tc.kind)), nil
			})
			if got := c.VipInfo(context.Background()); got != tc.want {
				t.Fatalf("VIP=%t want %t", got, tc.want)
			}
		})
	}
}

func TestPikPakPlaybackAndDownloadDetailsAreSeparate(t *testing.T) {
	c := newClient("token", "separate-usage", "separate-usage")
	calls := map[string]int{}
	c.http.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/drive/v1/privilege/vip" {
			return pikpakResponse(r, 200, `{"data":{"status":"ok","type":"platinum"}}`), nil
		}
		usage := r.URL.Query().Get("usage")
		calls[usage]++
		if usage == "FETCH" {
			return pikpakResponse(r, 200, `{"id":"separate","mime_type":"video/x-matroska","web_content_link":"https://cdn.example/original","size":"42"}`), nil
		}
		return pikpakResponse(r, 200, `{"id":"separate","mime_type":"video/x-matroska","web_content_link":"https://cdn.example/original","medias":[{"video":{"height":1080,"video_type":"mpegts"},"link":{"url":"https://cdn.example/transcode"}}]}`), nil
	})
	if _, _, err := c.DownloadURL(context.Background(), "separate"); err != nil {
		t.Fatal(err)
	}
	p, err := c.PlayInfo(context.Background(), "separate")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Qualities) != 2 || p.CurrentQuality != "FHD" {
		t.Fatalf("unexpected qualities/default: %+v", p)
	}
	if p.Qualities[0].Type != "mkv" || p.Qualities[1].Type != "ts" {
		t.Fatal("containers misidentified")
	}
	if _, _, err := c.DownloadURL(context.Background(), "separate"); err != nil {
		t.Fatal(err)
	}
	if calls["FETCH"] != 1 || calls["CACHE"] != 1 {
		t.Fatalf("cache usage mixed: %v", calls)
	}
}

func TestParseAPIErrorClassifiesRiskControl(t *testing.T) {
	err := parseAPIError([]byte(`{"error":"AccessProhibited"}`), http.StatusForbidden)
	var prohibited *PikPakAccessProhibitedError
	if !errors.As(err, &prohibited) {
		t.Fatalf("error = %v, want access-prohibited classification", err)
	}
	if err := parseAPIError([]byte(`{"message":"Your operation is too frequent, please try again later"}`), http.StatusBadRequest); err == nil {
		t.Fatal("too-frequent response returned nil error")
	} else {
		var rate *PikPakRateLimitError
		if !errors.As(err, &rate) || rate.RetryAfterSeconds < pikpakMinRateLimitSeconds {
			t.Fatalf("error = %v, want rate-limit classification", err)
		}
	}
}

func TestCaptchaInitRetriesConnectionEOFOnce(t *testing.T) {
	for _, failAlways := range []bool{false, true} {
		calls := 0
		hc := netx.NewClient(time.Second)
		hc.HTTP.Transport = pikpakRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["device_id"] != "device" {
				t.Fatalf("request body=%v error=%v", body, err)
			}
			if calls == 1 || failAlways {
				return nil, io.EOF
			}
			return pikpakResponse(r, 200, `{"captcha_token":"token"}`), nil
		})
		token, _, err := initCaptcha(context.Background(), hc, "device", "user", "POST:/v1/auth/signin", "")
		if calls != 2 || (err != nil) != failAlways || (!failAlways && token != "token") {
			t.Fatalf("calls=%d token=%s err=%v", calls, token, err)
		}
	}
}

func TestRateLimitErrorExposesAtLeastMinimumCooldown(t *testing.T) {
	var _ interface{ RetryAfter() time.Duration } = (*PikPakRateLimitError)(nil)
	if got := (&PikPakRateLimitError{RetryAfterSeconds: 1}).RetryAfter(); got < time.Duration(pikpakMinRateLimitSeconds)*time.Second {
		t.Fatalf("RetryAfter() = %v, want at least %ds", got, pikpakMinRateLimitSeconds)
	}
}

func TestPikPakCooldownIsScopedToLoginAccount(t *testing.T) {
	ResetPikPakLoginCooldown()
	t.Cleanup(ResetPikPakLoginCooldown)
	rememberPikPakLoginCooldown("first@example.com", &PikPakRateLimitError{RetryAfterSeconds: 60})
	if err := pikpakLoginCooldownError("first@example.com"); err == nil {
		t.Fatal("the rate-limited account should remain in cooldown")
	}
	if err := pikpakLoginCooldownError("second@example.com"); err != nil {
		t.Fatalf("a different PikPak account was blocked by the first account: %v", err)
	}
}

func TestPikPakRiskCooldownIsLongAndScopedToLoginAccount(t *testing.T) {
	ResetPikPakLoginCooldown()
	t.Cleanup(ResetPikPakLoginCooldown)
	rememberPikPakLoginCooldown("first@example.com", &PikPakAccessProhibitedError{})

	err := pikpakLoginCooldownError("first@example.com")
	var rate *PikPakRateLimitError
	if !errors.As(err, &rate) || rate.RetryAfterSeconds < pikpakRiskControlCooldownSeconds {
		t.Fatalf("risk cooldown = %v, want at least %d seconds", err, pikpakRiskControlCooldownSeconds)
	}
	if err := pikpakLoginCooldownError("second@example.com"); err != nil {
		t.Fatalf("a different PikPak account was blocked by risk control: %v", err)
	}
}

func TestPikPakVerifiedCaptchaConfirmsOnceBeforeSignIn(t *testing.T) {
	ResetPikPakLoginCooldown()
	t.Cleanup(ResetPikPakLoginCooldown)

	oldWait := pikpakVerifiedCaptchaWait
	pikpakVerifiedCaptchaWait = 0
	t.Cleanup(func() { pikpakVerifiedCaptchaWait = oldWait })
	oldStore := deviceStore
	deviceStore = &storePath{dir: t.TempDir()}
	t.Cleanup(func() { deviceStore = oldStore })
	oldTransport := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = oldTransport })

	confirmCalls := 0
	signInCalls := 0
	netx.TestTransportHook = pikpakRoundTripper(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host + req.URL.Path {
		case "user.mypikpak.com/v1/shield/captcha/init":
			confirmCalls++
			var body struct {
				CaptchaToken string `json:"captcha_token"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Errorf("decode captcha confirmation: %v", err)
			}
			if body.CaptchaToken != "initial-token" {
				t.Errorf("confirmation token = %q, want initial-token", body.CaptchaToken)
			}
			return pikpakResponse(req, http.StatusOK, `{"captcha_token":"confirmed-token"}`), nil
		case "user.mypikpak.com/v1/auth/signin":
			signInCalls++
			if got := req.Header.Get("X-Captcha-Token"); got != "confirmed-token" {
				t.Errorf("signin captcha token = %q, want confirmed-token", got)
			}
			return pikpakResponse(req, http.StatusOK, `{"access_token":"access","refresh_token":"refresh","expires_in":3600,"token_type":"Bearer","sub":"account-1","user_name":"Tester"}`), nil
		case "api-drive.mypikpak.com/drive/v1/about":
			return pikpakResponse(req, http.StatusOK, `{"quota":{"limit":100,"used":25}}`), nil
		default:
			return pikpakResponse(req, http.StatusNotFound, `{}`), nil
		}
	})

	token, err := authSignIn(context.Background(), drive.AuthRequest{Config: map[string]string{
		"username":                      "first@example.com",
		"password":                      "secret",
		"captcha_token":                 "initial-token",
		"captcha_verified":              "true",
		"captcha_requires_confirmation": "true",
		"captcha_redirect_uri":          "http://127.0.0.1:9000/callback",
	}})
	if err != nil {
		t.Fatalf("authSignIn returned error: %v", err)
	}
	if confirmCalls != 1 || signInCalls != 1 {
		t.Fatalf("request counts = confirmation:%d signin:%d, want one each", confirmCalls, signInCalls)
	}
	if token == nil || token.ProviderAccountID != "account-1" || token.UsedSize != 25 || token.TotalSize != 100 {
		t.Fatalf("token = %+v", token)
	}
}

func TestPikPakDeviceIDIsStableAndIndependentPerAccount(t *testing.T) {
	dir := t.TempDir()
	oldStore := deviceStore
	deviceStore = &storePath{dir: dir}
	t.Cleanup(func() { deviceStore = oldStore })

	first := getOrCreateDeviceID("First@example.com")
	firstAgain := getOrCreateDeviceID("first@example.com")
	second := getOrCreateDeviceID("second@example.com")
	if len(first) != 32 || !isHex(first) || first != firstAgain {
		t.Fatalf("device id is not stable per account: first=%q again=%q", first, firstAgain)
	}
	if first == second {
		t.Fatalf("different accounts received the same device id: %q", first)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("device identity directory was not persisted: %v", err)
	}
}

func TestPikPakVerifiedCaptchaWaitsForServerRegistration(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		readyAt, failAt, wantCalls int
	}{
		{"delayed-registration", 3, 0, 3},
		{"bounded-challenge", 99, 0, 4},
		{"rate-limit-stops-exchange", 99, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ResetPikPakLoginCooldown()
			t.Cleanup(ResetPikPakLoginCooldown)
			oldWait := pikpakVerifiedCaptchaWait
			pikpakVerifiedCaptchaWait = 0
			t.Cleanup(func() { pikpakVerifiedCaptchaWait = oldWait })
			previousTransport := netx.TestTransportHook
			t.Cleanup(func() { netx.TestTransportHook = previousTransport })
			calls, signins := 0, 0
			deviceID := ""
			netx.TestTransportHook = pikpakRoundTripper(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v1/shield/captcha/init":
					calls++
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body["captcha_token"] != fmt.Sprintf("chain-%d", calls-1) {
						t.Errorf("exchange %d did not carry the latest token", calls)
					}
					if deviceID == "" {
						deviceID = req.Header.Get("X-Device-Id")
					}
					if req.Header.Get("X-Device-Id") != deviceID {
						t.Error("device changed during exchange")
					}
					if calls == tc.failAt {
						return pikpakResponse(req, http.StatusTooManyRequests, `{"error":"too_many_requests"}`), nil
					}
					challenge := ""
					if calls < tc.readyAt {
						challenge = `,"url":"https://captcha.example/slider"`
					}
					return pikpakResponse(req, http.StatusOK, fmt.Sprintf(`{"captcha_token":"chain-%d"%s}`, calls, challenge)), nil
				case "/v1/auth/signin":
					signins++
					if req.Header.Get("X-Captcha-Token") != fmt.Sprintf("chain-%d", tc.readyAt) {
						t.Error("signin used an unconfirmed token")
					}
					return pikpakResponse(req, http.StatusOK, `{"access_token":"access","sub":"delayed-account"}`), nil
				case "/drive/v1/about":
					return pikpakResponse(req, http.StatusOK, `{}`), nil
				default:
					return nil, fmt.Errorf("unexpected request %s", req.URL.Path)
				}
			})
			_, err := authSignIn(context.Background(), drive.AuthRequest{Config: map[string]string{
				"username": "delayed@example.com", "password": "test",
				"captcha_token": "chain-0", "captcha_verified": "true", "captcha_requires_confirmation": "true",
			}})
			if calls != tc.wantCalls {
				t.Fatalf("confirmation calls=%d, want %d; err=%v", calls, tc.wantCalls, err)
			}
			if tc.readyAt <= tc.wantCalls {
				if err != nil || signins != 1 {
					t.Fatalf("delayed login: %v, signin calls=%d", err, signins)
				}
			} else {
				if err == nil || signins != 0 {
					t.Fatalf("unconfirmed login: %v, signin calls=%d", err, signins)
				}
				if tc.failAt == 0 {
					var challenge *CaptchaRequiredError
					if !errors.As(err, &challenge) || challenge.Token != "chain-4" {
						t.Fatalf("final challenge lost: %v", err)
					}
				}
			}
		})
	}
}

func TestAPIParentIDNormalizesRootSentinels(t *testing.T) {
	for _, value := range []string{"", "root", RootID, "/", "*"} {
		if got := apiParentID(value); got != "" {
			t.Fatalf("apiParentID(%q) = %q, want empty root parent", value, got)
		}
	}
	if got := apiParentID("folder-123"); got != "folder-123" {
		t.Fatalf("apiParentID(folder-123) = %q, want folder-123", got)
	}
}

func TestRootIDNormalizesProviderRoot(t *testing.T) {
	for _, value := range []string{"", "root", RootID, "/"} {
		if got := rootID(value); got != RootID {
			t.Fatalf("rootID(%q) = %q, want %q", value, got, RootID)
		}
	}
}

func TestStreamTypeUsesExplicitHintsAndExtensions(t *testing.T) {
	cases := map[string]struct {
		url  string
		hint string
		want string
	}{
		"generic stream stays mp4": {url: "https://cdn.example/video.mp4", hint: "stream", want: "mp4"},
		"HLS MIME":                 {url: "https://cdn.example/token", hint: "application/vnd.apple.mpegurl", want: "hls"},
		"DASH extension":           {url: "https://cdn.example/video.mpd?signature=secret", want: "dash"},
		"Matroska extension":       {url: "https://cdn.example/video.mkv", want: "mkv"},
		"RealMedia MIME":           {url: "https://cdn.example/token", hint: "video/vnd.rn-realvideo", want: "rmvb"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := streamType(tc.url, tc.hint); got != tc.want {
				t.Fatalf("streamType(%q, %q) = %q, want %q", tc.url, tc.hint, got, tc.want)
			}
		})
	}
}

type pikpakRoundTripper func(*http.Request) (*http.Response, error)

func (f pikpakRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func pikpakResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestCreateShareUsesPikPakDriveAPI(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	netx.TestTransportHook = pikpakRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api-drive.mypikpak.com" || req.URL.Path != "/drive/v1/share" {
			return nil, errors.New("unexpected PikPak share request")
		}
		if req.Header.Get("Authorization") != "Bearer access-token" || req.Header.Get("X-Device-Id") != "device-test" {
			return nil, errors.New("PikPak share request missing authentication headers")
		}
		var body struct {
			FileIDs        []string `json:"file_ids"`
			ShareTo        string   `json:"share_to"`
			ExpirationDays int      `json:"expiration_days"`
			PassCodeOption string   `json:"pass_code_option"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if strings.Join(body.FileIDs, ",") != "file-1,folder-1" || body.ShareTo != "encryptedlink" || body.ExpirationDays != 7 || body.PassCodeOption != "REQUIRED" {
			return nil, errors.New("unexpected PikPak share body")
		}
		return pikpakResponse(req, http.StatusOK, `{"share_id":"share-pikpak","share_url":"https://mypikpak.com/s/share-pikpak","pass_code":"p4ss","expiration":"2030-01-01T00:00:00Z","file_ids":["file-1","folder-1"]}`), nil
	})

	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "pikpak:account-test", DriveID: "pikpak:account-test",
		Token: &model.TokenInfo{AccessToken: "access-token", DeviceID: "device-test", ProviderAccountID: "account-test"},
	}, drive.ShareParams{FileIDs: []string{"file-1", "folder-1"}, ShareName: "测试分享", Expiration: "7", Password: "p4ss"})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if item.ShareID != "share-pikpak" || item.ShareURL != "https://mypikpak.com/s/share-pikpak" || item.SharePwd != "p4ss" || len(item.FileIDList) != 2 {
		t.Fatalf("share = %+v", item)
	}
}

func TestPikPakCancelShareUsesBatchDelete(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	netx.TestTransportHook = pikpakRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api-drive.mypikpak.com" || req.URL.Path != "/drive/v1/share:batchDelete" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer access-token" || req.Header.Get("X-Device-Id") != "device-test" {
			return nil, errors.New("PikPak cancellation missing authentication headers")
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if strings.Join(body.IDs, ",") != "share-pikpak" {
			return nil, fmt.Errorf("cancellation body = %+v", body)
		}
		return pikpakResponse(req, http.StatusOK, `{}`), nil
	})

	err := (&Driver{}).CancelShare(context.Background(), drive.Context{
		Token: &model.TokenInfo{AccessToken: "access-token", DeviceID: "device-test", ProviderAccountID: "account-test"},
	}, model.ShareHistoryEntry{ShareID: "share-pikpak"})
	if err != nil {
		t.Fatalf("CancelShare() error = %v", err)
	}
}
