package pan123

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

// ---- signPath / crc32 ----

func TestUploadCanceledContextCannotReportSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canceled.txt")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ui := &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: path, Name: "canceled.txt"}}
	err := (&Driver{}).UploadOneFile(ctx, drive.Context{}, ui)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload returned %v, want context.Canceled", err)
	}
	if ui.Upload.IsCompleted {
		t.Fatal("canceled upload was marked completed")
	}
}

func TestCRC32StandardVector(t *testing.T) {
	if got := crc32sum("123456789"); got != 0xCBF43926 {
		t.Fatalf("crc32sum(123456789) = %08x, want cbf43926", got)
	}
}

func TestSignTableDigitMapping(t *testing.T) {
	// digits 0-9 must map to table[0..9] = "adefghlmyi"
	want := map[byte]byte{
		'0': 'a', '1': 'd', '2': 'e', '3': 'f', '4': 'g',
		'5': 'h', '6': 'l', '7': 'm', '8': 'y', '9': 'i',
	}
	for d, w := range want {
		if signTable[d-'0'] != w {
			t.Fatalf("signTable[%c] = %c, want %c", d, signTable[d-'0'], w)
		}
	}
}

var numPartsRe = regexp.MustCompile(`^\d+-\d+-\d+$`)

func TestSignPathStructure(t *testing.T) {
	timeSign, dataSign := signPath("/b/api/file/list/new")
	if !regexp.MustCompile(`^\d+$`).MatchString(timeSign) {
		t.Fatalf("timeSign = %q, want decimal string", timeSign)
	}
	if !numPartsRe.MatchString(dataSign) {
		t.Fatalf("dataSign = %q, want ts-random-dataSign", dataSign)
	}
	// different paths (with same wall clock) must produce distinct dataSign
	// because apiPath feeds crc32 — verify the value differs for a different path.
	_, dataSign2 := signPath("/b/api/file/rename")
	if dataSign == dataSign2 {
		t.Fatalf("dataSign identical across different paths: %q", dataSign)
	}
}

func TestWithAPISign(t *testing.T) {
	raw := "https://yun.123pan.com/b/api/file/list/new"
	signed := withAPISign(raw)
	if !strings.HasPrefix(signed, raw+"?") {
		t.Fatalf("signed url = %q, want prefix %q", signed, raw+"?")
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Query()) != 1 {
		t.Fatalf("expected exactly 1 sign param, got %d", len(u.Query()))
	}
	for k, v := range u.Query() {
		if len(v) != 1 {
			t.Fatalf("param %s has %d values", k, len(v))
		}
		if !regexp.MustCompile(`^\d+$`).MatchString(k) {
			t.Fatalf("sign key %q is not decimal", k)
		}
		if !numPartsRe.MatchString(v[0]) {
			t.Fatalf("sign value %q is not ts-random-dataSign", v[0])
		}
	}
	// query params must not break signing
	signed2 := withAPISign(raw + "?driveId=0")
	if !strings.Contains(signed2, "driveId=0") {
		t.Fatalf("existing query lost: %q", signed2)
	}
}

// ---- id helpers ----

func TestToPan123FileID(t *testing.T) {
	cases := map[string]string{
		"":            "0",
		"pan123_root": "0",
		"root":        "0",
		"/":           "0",
		"0":           "0",
		"123":         "123",
		" 42 ":        "42",
	}
	for in, want := range cases {
		if got := toPan123FileID(in); got != want {
			t.Errorf("toPan123FileID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToPan123Number(t *testing.T) {
	if toPan123Number("0") != 0 || toPan123Number("pan123_root") != 0 || toPan123Number("/") != 0 {
		t.Fatal("root sentinels must map to 0")
	}
	if toPan123Number("42") != 42 {
		t.Fatal("numeric id must parse")
	}
	if toPan123Number("abc") != 0 {
		t.Fatal("non-numeric id must map to 0")
	}
}

func TestParentOf(t *testing.T) {
	if parentOf("") != RootID || parentOf("0") != RootID {
		t.Fatal("empty/0 parent must map to root sentinel")
	}
	if parentOf("5") != "5" {
		t.Fatal("numeric parent kept")
	}
}

// ---- normalization ----

func jnum(s string) json.Number { return json.Number(s) }

func TestNormalizePan123FilePascalCase(t *testing.T) {
	raw := map[string]any{
		"FileId":       jnum("12345678901234567"),
		"FileName":     "a.mp4",
		"Size":         jnum("1024"),
		"Type":         jnum("0"),
		"Etag":         "abc",
		"S3KeyFlag":    "S3F",
		"DownloadUrl":  "https://d.example/a",
		"UpdateAt":     "2026-01-02T03:04:05Z",
		"ParentFileId": jnum("9"),
	}
	f := normalizePan123File(raw)
	if f.FileID != "12345678901234567" {
		t.Errorf("FileID = %q", f.FileID)
	}
	if f.FileName != "a.mp4" || f.Size != 1024 || f.Type != 0 || f.Etag != "abc" || f.S3KeyFlag != "S3F" {
		t.Errorf("fields mismatch: %+v", f)
	}
	if f.ParentFileID != "9" {
		t.Errorf("ParentFileID = %q", f.ParentFileID)
	}
}

func TestNormalizePan123FileCamelCaseAndS3Fallback(t *testing.T) {
	raw := map[string]any{
		"fileId":    "7",
		"fileName":  "b.txt",
		"size":      jnum("2"),
		"type":      jnum("1"),
		"s3keyFlag": "S3B",
	}
	f := normalizePan123File(raw)
	if f.FileID != "7" || f.FileName != "b.txt" || f.Type != 1 || f.S3KeyFlag != "S3B" {
		t.Errorf("camelCase normalize failed: %+v", f)
	}
	// pickS3 regex fallback
	raw2 := map[string]any{"fileId": "8", "S3_key_flag": "X"}
	f2 := normalizePan123File(raw2)
	if f2.S3KeyFlag != "X" {
		t.Errorf("pickS3 regex fallback failed: %q", f2.S3KeyFlag)
	}
}

func TestNormalizePan123FileStringFileID(t *testing.T) {
	raw := map[string]any{"FileId": "999", "FileName": "c"}
	f := normalizePan123File(raw)
	if f.FileID != "999" {
		t.Errorf("string FileID = %q", f.FileID)
	}
}

func TestParseMapUseNumber(t *testing.T) {
	m := parseMap(json.RawMessage(`{"FileId": 12345678901234567, "nested": {"k": 1}}`))
	if got := asString(m["FileId"]); got != "12345678901234567" {
		t.Fatalf("big FileId lost precision: %q", got)
	}
}

// ---- pool merge ----

func TestMergeFileEmptyIncomingKeepsExisting(t *testing.T) {
	ex := pan123File{FileID: "1", FileName: "a", Size: 10, Type: 1, Etag: "e", S3KeyFlag: "S", DownloadURL: "u", UpdateAt: "t"}
	in := pan123File{FileID: "1", FileName: "", Size: 0, Type: 0, Etag: "", S3KeyFlag: "", DownloadURL: "", UpdateAt: ""}
	got := mergeFile(in, ex)
	want := pan123File{FileID: "1", FileName: "a", Size: 10, Type: 0, Etag: "e", S3KeyFlag: "S", DownloadURL: "u", UpdateAt: "t"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeFile = %+v, want %+v", got, want)
	}
}

func TestMergeFileIncomingWins(t *testing.T) {
	ex := pan123File{FileID: "1", FileName: "a", Size: 10, S3KeyFlag: "S"}
	in := pan123File{FileID: "1", FileName: "b", Size: 20, Etag: "e2", S3KeyFlag: "S2"}
	got := mergeFile(in, ex)
	if got.FileName != "b" || got.Size != 20 || got.S3KeyFlag != "S2" || got.Etag != "e2" {
		t.Fatalf("incoming should win: %+v", got)
	}
}

func TestPutPoolAndGet(t *testing.T) {
	c := drive.Context{DriveID: "drive-a"}
	putPool(c, pan123File{FileID: "55", FileName: "x", S3KeyFlag: "S"})
	f, ok := poolGet(c, "55")
	if !ok || f.FileName != "x" || f.S3KeyFlag != "S" {
		t.Fatalf("pool get failed: %+v ok=%v", f, ok)
	}
	if _, ok2 := poolGet(c, "pan123_root"); ok2 {
		t.Fatal("root id must not be pooled")
	}
}

func TestPutPoolIsolatedByDrive(t *testing.T) {
	first := drive.Context{DriveID: "drive-a"}
	second := drive.Context{DriveID: "drive-b"}
	putPool(first, pan123File{FileID: "77", FileName: "first", S3KeyFlag: "A"})
	putPool(second, pan123File{FileID: "77", FileName: "second", S3KeyFlag: "B"})
	a, ok := poolGet(first, "77")
	if !ok || a.FileName != "first" || a.S3KeyFlag != "A" {
		t.Fatalf("first drive pool entry leaked: %+v ok=%v", a, ok)
	}
	b, ok := poolGet(second, "77")
	if !ok || b.FileName != "second" || b.S3KeyFlag != "B" {
		t.Fatalf("second drive pool entry leaked: %+v ok=%v", b, ok)
	}
}

// ---- description backup ----

func TestPan123MetaDescRoundTrip(t *testing.T) {
	f := pan123File{FileID: "3", FileName: "f.bin", Size: 99, Type: 0, Etag: "abcd", S3KeyFlag: "KEY"}
	desc := encodePan123MetaDesc(f)
	if !strings.HasPrefix(desc, "pan123meta:") {
		t.Fatalf("desc = %q", desc)
	}
	got, ok := decodePan123MetaDesc(desc)
	if !ok {
		t.Fatal("decode failed")
	}
	if !reflect.DeepEqual(got, f) {
		t.Fatalf("round trip = %+v, want %+v", got, f)
	}
	// empty backup
	if encodePan123MetaDesc(pan123File{FileID: "1"}) != "" {
		t.Fatal("empty meta must not produce backup")
	}
	// junk input
	if _, ok := decodePan123MetaDesc("pan123meta:!!!invalid"); ok {
		t.Fatal("invalid backup must not decode")
	}
}

func TestResolveAListFileUsesCachedDescription(t *testing.T) {
	drive.ClearFileMetaCache()
	t.Cleanup(drive.ClearFileMetaCache)

	c := drive.Context{UserID: "pan123:cached-user", DriveID: "pan123:cached-drive"}
	listed := pan123File{
		FileID:    "123456",
		FileName:  "cached.mp4",
		Size:      128,
		Etag:      "etag-cached",
		S3KeyFlag: "s3-key-cached",
	}
	drive.RememberListedFiles(c.UserID, c.DriveID, "0", []model.File{mapFile(listed, c.DriveID, "0")})

	got, err := (&Driver{}).resolveAListFile(context.Background(), c, listed.FileID)
	if err != nil {
		t.Fatalf("resolveAListFile returned error: %v", err)
	}
	if got == nil || got.S3KeyFlag != listed.S3KeyFlag {
		t.Fatalf("resolved file = %+v, want S3KeyFlag %q", got, listed.S3KeyFlag)
	}
	if got.FileName != listed.FileName || got.Etag != listed.Etag || got.Size != listed.Size {
		t.Fatalf("resolved metadata = %+v, want listed metadata", got)
	}
}

func TestResolveAListFileCachedDescriptionIsolatedByDrive(t *testing.T) {
	drive.ClearFileMetaCache()
	t.Cleanup(drive.ClearFileMetaCache)

	first := drive.Context{UserID: "pan123:account-a", DriveID: "pan123:account-a"}
	second := drive.Context{UserID: "pan123:account-b", DriveID: "pan123:account-b"}
	firstFile := pan123File{FileID: "654321", FileName: "a.mp4", Etag: "etag-a", S3KeyFlag: "key-a"}
	secondFile := pan123File{FileID: "654321", FileName: "b.mp4", Etag: "etag-b", S3KeyFlag: "key-b"}
	drive.RememberListedFiles(first.UserID, first.DriveID, "0", []model.File{mapFile(firstFile, first.DriveID, "0")})
	drive.RememberListedFiles(second.UserID, second.DriveID, "0", []model.File{mapFile(secondFile, second.DriveID, "0")})

	a, err := (&Driver{}).resolveAListFile(context.Background(), first, firstFile.FileID)
	if err != nil {
		t.Fatalf("first resolveAListFile returned error: %v", err)
	}
	b, err := (&Driver{}).resolveAListFile(context.Background(), second, secondFile.FileID)
	if err != nil {
		t.Fatalf("second resolveAListFile returned error: %v", err)
	}
	if a.S3KeyFlag != firstFile.S3KeyFlag || b.S3KeyFlag != secondFile.S3KeyFlag {
		t.Fatalf("cached metadata leaked across drives: first=%+v second=%+v", a, b)
	}
}

func TestPan123VideoPreviewUsesCachedDescription(t *testing.T) {
	drive.ClearFileMetaCache()
	t.Cleanup(drive.ClearFileMetaCache)

	c := drive.Context{
		UserID:  "pan123:preview-user",
		DriveID: "pan123:preview-drive",
		Token:   &model.TokenInfo{AccessToken: "access-token"},
	}
	listed := pan123File{FileID: "777", FileName: "movie.mp4", Size: 4096, Etag: "etag-preview", S3KeyFlag: "key-preview"}
	drive.RememberListedFiles(c.UserID, c.DriveID, "0", []model.File{mapFile(listed, c.DriveID, "0")})
	var redirectServer *httptest.Server
	redirectServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redirect" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", redirectServer.URL+"/cdn/movie.mp4")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(redirectServer.Close)

	previous := netx.TestTransportHook
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/file/download_info") {
			return pan123JSONResponse(fmt.Sprintf(`{"code":0,"data":{"DownloadUrl":%q}}`, redirectServer.URL+"/redirect"), req), nil
		}
		return pan123JSONResponse(`{"code":0}`, req), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	preview, err := (&Driver{}).GetVideoPreview(context.Background(), c, listed.FileID)
	if err != nil {
		t.Fatalf("GetVideoPreview returned error: %v", err)
	}
	if preview == nil || len(preview.Qualities) != 1 || preview.Qualities[0].URL != redirectServer.URL+"/cdn/movie.mp4" {
		t.Fatalf("preview = %+v, want cached-file redirect", preview)
	}
}

// ---- etag / duplicate ----

func TestEtagAsMD5(t *testing.T) {
	if etagAsMd5("ABCDEF01234567890123456789abcdef") != "abcdef01234567890123456789abcdef" {
		t.Fatal("uppercase md5 must be lowercased")
	}
	if etagAsMd5("xyz") != "" || etagAsMd5(strings.Repeat("a", 31)) != "" || etagAsMd5(strings.Repeat("g", 32)) != "" {
		t.Fatal("invalid etags must be rejected")
	}
}

func TestDuplicateFromRequest(t *testing.T) {
	if duplicateFromRequest(2) != 2 || duplicateFromRequest(0) != 1 || duplicateFromRequest(1) != 1 {
		t.Fatal("duplicate mapping must be 2→2 else 1")
	}
}

func TestDuplicateFromPolicy(t *testing.T) {
	cases := map[string]int{
		"":          2,
		"overwrite": 2,
		"rename":    1,
		"refuse":    1,
		"skip":      1,
		"unknown":   2,
	}
	for policy, want := range cases {
		if got := duplicateFromPolicy(policy); got != want {
			t.Errorf("duplicateFromPolicy(%q) = %d, want %d", policy, got, want)
		}
	}
}

// ---- expiration ----

func TestFormatPan123Time(t *testing.T) {
	ts := time.Date(2026, 1, 2, 15, 4, 5, 0, time.Local)
	if got := formatPan123Time(ts); got != "2026-01-02 15:04:05" {
		t.Fatalf("formatPan123Time = %q", got)
	}
}

func TestFormatPan123Expiration(t *testing.T) {
	if got := formatPan123Expiration(""); got != pan123PermanentShareExpiration {
		t.Fatalf("permanent expiration = %q", got)
	}
	if formatPan123Expiration("not-a-date") != "" {
		t.Fatal("invalid expiration must be empty")
	}
	// Zone-less values are interpreted as local time and normalized to the API's
	// explicit UTC+08:00 form.
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local).In(time.FixedZone("CST", 8*60*60)).Format(time.RFC3339)
	if got := formatPan123Expiration("2026-03-04 05:06:07"); got != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

func TestParseFlexibleTime(t *testing.T) {
	if got := parseFlexibleTime("2026-01-02T15:04:05Z"); got.IsZero() || got.Unix() != time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC).Unix() {
		t.Fatalf("RFC3339 parse failed: %v", got)
	}
	if got := parseFlexibleTime("bogus"); !got.IsZero() {
		t.Fatalf("invalid must be zero: %v", got)
	}
}

func TestGetExpiresTimeAMZ(t *testing.T) {
	base := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC).UnixMilli()
	url := "https://cdn.example.com/f?X-Amz-Date=20260102T150405Z&X-Amz-Expires=3600"
	want := base + 3600*1000
	if got := getExpiresTime(url); got != want {
		t.Fatalf("getExpiresTime = %d, want %d", got, want)
	}
}

func TestGetExpiresTimeNumeric(t *testing.T) {
	// second-level timestamp < 1e10 → ms
	if got := getExpiresTime("https://x/f?expire=1767225600"); got != 1767225600*1000 {
		t.Fatalf("seconds timestamp = %d", got)
	}
	// ms-level timestamp >= 1e10 → raw
	if got := getExpiresTime("https://x/f?expires=2000000000000"); got != 2000000000000 {
		t.Fatalf("ms timestamp = %d", got)
	}
	// small numbers (business params) skipped
	if got := getExpiresTime("https://x/f?e=1"); got != 0 {
		t.Fatalf("small value must be skipped, got %d", got)
	}
	// date string parse
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local).UnixMilli()
	if got := getExpiresTime("https://x/f?expire=2026-01-02"); got != want {
		t.Fatalf("date string = %d, want %d", got, want)
	}
}

func TestGetExpiresTimeNoParams(t *testing.T) {
	if got := getExpiresTime("https://x/f"); got != 0 {
		t.Fatalf("no params must yield 0, got %d", got)
	}
	if got := getExpiresTime(""); got != 0 {
		t.Fatalf("empty url must yield 0, got %d", got)
	}
}

// ---- redirect extraction ----

func TestExtractPan123RedirectURL(t *testing.T) {
	// JSON redirect_url
	got := extractPan123RedirectURL(`{"data":{"redirect_url":"https://cdn.example.com/a"}}`, "https://yun.123pan.com/x")
	if got != "https://cdn.example.com/a" {
		t.Fatalf("json redirect = %q", got)
	}
	// JSON redirectUrl
	got = extractPan123RedirectURL(`{"data":{"redirectUrl":"https://cdn.example.com/b"}}`, "https://yun.123pan.com/x")
	if got != "https://cdn.example.com/b" {
		t.Fatalf("json redirectUrl = %q", got)
	}
	// HTML href fallback
	got = extractPan123RedirectURL(`<html><a href="https://cdn.example.com/c">go</a></html>`, "https://yun.123pan.com/x")
	if got != "https://cdn.example.com/c" {
		t.Fatalf("href redirect = %q", got)
	}
	// invalid scheme rejected
	if got := extractPan123RedirectURL(`{"data":{"redirect_url":"ftp://x/y"}}`, "https://yun.123pan.com/x"); got != "" {
		t.Fatalf("ftp scheme must be rejected, got %q", got)
	}
	// relative href (no scheme) rejected by regex
	if got := extractPan123RedirectURL(`<a href="/rel">x</a>`, "https://yun.123pan.com/x"); got != "" {
		t.Fatalf("relative href must be rejected, got %q", got)
	}
	// empty
	if got := extractPan123RedirectURL("", "https://yun.123pan.com/x"); got != "" {
		t.Fatalf("empty body must yield empty, got %q", got)
	}
}

func TestDecodePan123ParamsURL(t *testing.T) {
	const target = "https://x.test/?q=???"
	cases := []struct {
		name  string
		value string
	}{
		{name: "standard padded", value: base64.StdEncoding.EncodeToString([]byte(target))},
		{name: "standard raw", value: base64.RawStdEncoding.EncodeToString([]byte(target))},
		{name: "url padded", value: base64.URLEncoding.EncodeToString([]byte(target))},
		{name: "url raw", value: base64.RawURLEncoding.EncodeToString([]byte(target))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodePan123ParamsURL(tc.value); got != target {
				t.Fatalf("decodePan123ParamsURL(%q) = %q, want %q", tc.value, got, target)
			}
		})
	}
}

// ---- login body ----

func TestPan123LoginBody(t *testing.T) {
	email := pan123LoginBody("a@b.com", "p")
	if email["mail"] != "a@b.com" || email["password"] != "p" || email["type"] != 2 {
		t.Fatalf("email body = %+v", email)
	}
	phone := pan123LoginBody("13800000000", "p")
	if phone["passport"] != "13800000000" || phone["remember"] != true {
		t.Fatalf("phone body = %+v", phone)
	}
}

func TestPan123CodeAcceptsNumericStrings(t *testing.T) {
	var resp apiResp
	if err := json.Unmarshal([]byte(`{"code":"401","message":"expired"}`), &resp); err != nil {
		t.Fatalf("numeric string code must decode: %v", err)
	}
	if resp.Code != 401 || resp.Message != "expired" {
		t.Fatalf("decoded response = %+v", resp)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCreateShareUsesPan123API(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "www.123pan.com" || req.URL.Path != "/a/api/share/create" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer access-token" {
			return nil, errors.New("123 share request missing authorization")
		}
		if req.Header.Get("Origin") != "https://www.123pan.com" || req.Header.Get("Referer") != "https://www.123pan.com/" {
			return nil, fmt.Errorf("123 share origin headers = origin:%q referer:%q", req.Header.Get("Origin"), req.Header.Get("Referer"))
		}
		query := req.URL.Query()
		if len(query) != 1 {
			return nil, fmt.Errorf("123 share request sign query = %v", query)
		}
		for key, values := range query {
			if !regexp.MustCompile(`^\d+$`).MatchString(key) || len(values) != 1 || !numPartsRe.MatchString(values[0]) {
				return nil, fmt.Errorf("123 share request invalid sign %q=%v", key, values)
			}
		}
		var body struct {
			FileIDs    string `json:"fileIdList"`
			ShareName  string `json:"shareName"`
			SharePwd   string `json:"sharePwd"`
			Expiration string `json:"expiration"`
			Event      string `json:"event"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body.FileIDs != "101,202" || body.ShareName != "测试分享" || body.SharePwd != "p4ss" || body.Expiration == "" || body.Event != "shareCreate" {
			return nil, fmt.Errorf("share body = %+v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"ShareKey":"share-123","SharePwd":"p4ss"}}`)),
			Request:    req,
		}, nil
	})

	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "pan123:user", DriveID: "pan123:user", Token: &model.TokenInfo{AccessToken: "access-token"},
	}, drive.ShareParams{FileIDs: []string{"101", "202"}, ShareName: "测试分享", Expiration: time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339), Password: "p4ss"})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if item.ShareID != "share-123" || item.ShareURL != "https://www.123pan.com/s/share-123" || item.FileID != "101" || len(item.FileIDList) != 2 || item.AccountID != "pan123:user" {
		t.Fatalf("share = %+v", item)
	}
	if !(&Driver{}).Capabilities().CombinedShare {
		t.Fatal("123 云盘支持多文件分享，必须保留创建分享能力")
	}
}

func TestPan123LoginAcceptsNumericStringCode(t *testing.T) {
	previous := netx.TestTransportHook
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(body), `"mail":"user@example.com"`) || !strings.Contains(string(body), `"type":2`) {
			return nil, fmt.Errorf("unexpected login body: %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":"200","data":{"token":"fresh-token"}}`)),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	token, err := pan123Login(context.Background(), "user@example.com", "password")
	if err != nil || token != "fresh-token" {
		t.Fatalf("pan123Login token=%q err=%v", token, err)
	}
}

func TestPan123StoredCredentialsTrimUsernameOnly(t *testing.T) {
	tok := &model.TokenInfo{RefreshToken: `{"username":"  13800000000  ","password":" p "}`}
	username, password, ok := storedPan123Credentials(tok)
	if !ok || username != "13800000000" || password != " p " {
		t.Fatalf("stored credentials = username=%q password=%q ok=%v", username, password, ok)
	}

	if _, _, ok := storedPan123Credentials(&model.TokenInfo{RefreshToken: `{"username":" ","password":"p"}`}); ok {
		t.Fatal("blank stored username must be rejected")
	}
}

func TestCreateShareRejectsEmptyFileIDs(t *testing.T) {
	d := &Driver{}
	share, err := d.CreateShare(context.Background(), drive.Context{}, drive.ShareParams{})
	if err == nil || share != nil {
		t.Fatalf("empty file ids must be rejected before the API call: share=%+v err=%v", share, err)
	}
	if !strings.Contains(err.Error(), "至少选择一个文件") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveShareRejectsInvalidArguments(t *testing.T) {
	d := &Driver{}
	ids, err := d.SaveShare(context.Background(), drive.Context{}, nil, []string{"1"}, "0")
	if err == nil || ids != nil {
		t.Fatalf("nil share session must be rejected before the API call: ids=%v err=%v", ids, err)
	}
	if !strings.Contains(err.Error(), "分享会话无效") {
		t.Fatalf("unexpected nil-session error: %v", err)
	}

	ids, err = d.SaveShare(context.Background(), drive.Context{}, &drive.ShareImportSession{ShareKey: "key"}, nil, "0")
	if err == nil || ids != nil {
		t.Fatalf("empty file ids must be rejected before the API call: ids=%v err=%v", ids, err)
	}
	if !strings.Contains(err.Error(), "至少选择一个分享文件") {
		t.Fatalf("unexpected empty-file error: %v", err)
	}
}

// ---- chunk plan ----

func TestCalcPan123ChunkPlan(t *testing.T) {
	const mb = int64(1024 * 1024)
	cases := []struct {
		size       int64
		chunkSize  int64
		chunkCount int64
	}{
		{0, 1, 1},
		{100, 100, 1},
		{16 * mb, 16 * mb, 1},
		{16*mb + 1, 16 * mb, 2},
		{32 * mb, 16 * mb, 2},
		{33 * mb, 16 * mb, 3},
		{48*mb + 1, 16 * mb, 4},
	}
	for _, c := range cases {
		plan := calcPan123ChunkPlan(c.size)
		if plan.ChunkSize != c.chunkSize || plan.ChunkCount != c.chunkCount {
			t.Errorf("size=%d plan=%+v, want chunkSize=%d chunkCount=%d", c.size, plan, c.chunkSize, c.chunkCount)
		}
	}
}

// ---- mapping ----

func TestMapFile(t *testing.T) {
	// folder entry
	folder := pan123File{FileID: "1", FileName: "dir", Type: 1, Size: 0, UpdateAt: "2026-01-02T03:04:05Z"}
	f := mapFile(folder, "pan123:acc", "0")
	if !f.IsDir || f.Category != "folder" || f.Icon != "iconfile-folder" {
		t.Fatalf("folder mapping wrong: %+v", f)
	}
	if f.ParentFileID != RootID {
		t.Fatalf("parent must map to root sentinel, got %q", f.ParentFileID)
	}
	if f.SizeStr == "" || f.TimeStr == "" {
		t.Fatalf("size/time strings missing: %+v", f)
	}
	// file entry
	file := pan123File{FileID: "2", FileName: "v.mp4", Type: 0, Size: 1024, UpdateAt: "2026-01-02T03:04:05Z", S3KeyFlag: "S", Etag: "abc"}
	f2 := mapFile(file, "pan123:acc", "5")
	if f2.IsDir || f2.Category != "video" || f2.Ext != "mp4" {
		t.Fatalf("file mapping wrong: %+v", f2)
	}
	if f2.ParentFileID != "5" {
		t.Fatalf("numeric parent kept, got %q", f2.ParentFileID)
	}
	if f2.Description == "" {
		t.Fatal("description backup missing")
	}
}

// ---- upload request data ----

func TestParseUploadRequestData(t *testing.T) {
	raw := map[string]any{
		"Bucket": "b", "Key": "k", "FileId": jnum("7"), "Reuse": true, "StorageNode": "n", "UploadId": "u",
	}
	d := parseUploadRequestData(raw)
	if d.Bucket != "b" || d.Key != "k" || d.FileID != "7" || !d.Reuse || d.StorageNode != "n" || d.UploadID != "u" {
		t.Fatalf("parseUploadRequestData = %+v", d)
	}
}

func TestPan123UploadSessionRoundTripAndHashIsolation(t *testing.T) {
	data := UploadRequestData{Bucket: "bucket", Key: "object", FileID: "7", StorageNode: "node", UploadID: "upload-1"}
	encoded := encodePan123UploadSession(data)
	decoded, ok := decodePan123UploadSession(encoded)
	if !ok || decoded != data {
		t.Fatalf("upload session round trip = %+v ok=%v, want %+v", decoded, ok, data)
	}

	c := drive.Context{UserID: "pan123:user", DriveID: "pan123:user"}
	first := pan123UploadSessionKey(c, "0", "movie.mp4", 100, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	second := pan123UploadSessionKey(c, "0", "movie.mp4", 100, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if first == second {
		t.Fatal("upload session key must include the file MD5")
	}

	if _, ok := decodePan123UploadSession("upload-legacy"); ok {
		t.Fatal("legacy bare upload id must not be treated as a complete resumable session")
	}
}

type pan123MemoryUploadStore struct {
	sessionID string
	parts     []int
	cleared   bool
}

func (s *pan123MemoryUploadStore) SaveUploadSession(key string, parts []int) error {
	return s.SaveUploadSessionState(key, "", parts)
}

func (s *pan123MemoryUploadStore) LoadUploadSession(key string) []int {
	_, parts := s.LoadUploadSessionState(key)
	return parts
}

func (s *pan123MemoryUploadStore) ClearUploadSession(key string) {
	s.sessionID = ""
	s.parts = nil
	s.cleared = true
}

func (s *pan123MemoryUploadStore) SaveUploadSessionState(key, sessionID string, parts []int) error {
	s.sessionID = sessionID
	s.parts = append([]int(nil), parts...)
	return nil
}

func (s *pan123MemoryUploadStore) LoadUploadSessionState(key string) (string, []int) {
	return s.sessionID, append([]int(nil), s.parts...)
}

func TestPan123UploadResumesSavedSessionBeforeRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.txt")
	content := []byte("already uploaded")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := drive.Context{
		UserID:  "pan123:user",
		DriveID: "pan123:user",
		Token:   &model.TokenInfo{AccessToken: "access-token"},
	}
	etag, err := fileMD5(path, nil)
	if err != nil {
		t.Fatalf("fileMD5: %v", err)
	}
	data := UploadRequestData{Bucket: "bucket", Key: "object", FileID: "7", StorageNode: "node", UploadID: "upload-1"}
	key := pan123UploadSessionKey(c, "0", "resume.txt", int64(len(content)), etag)
	store := &pan123MemoryUploadStore{}
	_ = store.SaveUploadSessionState(key, encodePan123UploadSession(data), []int{1})
	drive.SetUploadSessionStore(store)
	t.Cleanup(func() { drive.SetUploadSessionStore(nil) })

	previous := netx.TestTransportHook
	requestCount := 0
	presignCount := 0
	completeCount := 0
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/file/upload_request"):
			requestCount++
		case strings.HasSuffix(req.URL.Path, "/file/s3_upload_object/auth"):
			presignCount++
			return pan123JSONResponse(`{"code":0,"data":{"presignedUrls":{}}}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/upload_complete/v2"):
			completeCount++
			return pan123JSONResponse(`{"code":0}`, req), nil
		}
		return pan123JSONResponse(`{"code":0,"data":{"Bucket":"new-bucket","Key":"new-key","FileId":"8","StorageNode":"new-node","UploadId":"new-upload"}}`, req), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path,
		ParentFileID:  "pan123_root",
		DriveID:       c.DriveID,
		Name:          "resume.txt",
		Size:          int64(len(content)),
	}}
	if err := (&Driver{}).UploadOneFile(context.Background(), c, ui); err != nil {
		t.Fatalf("UploadOneFile: %v", err)
	}
	if requestCount != 0 {
		t.Fatalf("upload_request called %d times while resuming", requestCount)
	}
	if presignCount != 1 || completeCount != 1 {
		t.Fatalf("presign=%d complete=%d, want one each", presignCount, completeCount)
	}
	if ui.Upload.UploadID != data.UploadID || ui.Upload.FileID != data.FileID || !ui.Upload.IsCompleted {
		t.Fatalf("upload state = %+v", ui.Upload)
	}
	if !store.cleared {
		t.Fatal("completed upload session was not cleared")
	}
}

func TestPan123UploadRecreatesStaleSavedSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stale.txt")
	content := []byte("stale session")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := drive.Context{
		UserID:  "pan123:user",
		DriveID: "pan123:user",
		Token:   &model.TokenInfo{AccessToken: "access-token"},
	}
	etag, err := fileMD5(path, nil)
	if err != nil {
		t.Fatalf("fileMD5: %v", err)
	}
	key := pan123UploadSessionKey(c, "0", "stale.txt", int64(len(content)), etag)
	store := &pan123MemoryUploadStore{}
	old := UploadRequestData{Bucket: "old-bucket", Key: "old-key", FileID: "1", StorageNode: "old-node", UploadID: "old-upload"}
	if err := store.SaveUploadSessionState(key, encodePan123UploadSession(old), nil); err != nil {
		t.Fatalf("save old session: %v", err)
	}
	drive.SetUploadSessionStore(store)
	t.Cleanup(func() { drive.SetUploadSessionStore(nil) })

	previous := netx.TestTransportHook
	requestCount := 0
	presignCount := 0
	completeCount := 0
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/file/upload_request"):
			requestCount++
			return pan123JSONResponse(`{"code":0,"data":{"Bucket":"new-bucket","Key":"new-key","FileId":"2","StorageNode":"new-node","UploadId":"new-upload"}}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/s3_upload_object/auth"):
			presignCount++
			if presignCount == 1 {
				return pan123JSONResponse(`{"code":1001,"message":"upload session expired"}`, req), nil
			}
			return pan123JSONResponse(`{"code":0,"data":{"presignedUrls":{"1":"https://upload.test/part/1"}}}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/upload_complete/v2"):
			completeCount++
			return pan123JSONResponse(`{"code":0}`, req), nil
		}
		return pan123JSONResponse(`{"code":0}`, req), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path,
		ParentFileID:  "pan123_root",
		DriveID:       c.DriveID,
		Name:          "stale.txt",
		Size:          int64(len(content)),
	}}
	if err := (&Driver{}).UploadOneFile(context.Background(), c, ui); err != nil {
		t.Fatalf("UploadOneFile: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("upload_request called %d times, want one fresh session", requestCount)
	}
	if presignCount != 2 || completeCount != 1 {
		t.Fatalf("presign=%d complete=%d, want two presigns and one complete", presignCount, completeCount)
	}
	if ui.Upload.UploadID != "new-upload" || ui.Upload.FileID != "2" || !ui.Upload.IsCompleted {
		t.Fatalf("upload state = %+v", ui.Upload)
	}
	if !store.cleared {
		t.Fatal("stale/completed upload session was not cleared")
	}
}

func TestPan123UploadFreshSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.txt")
	content := []byte("fresh upload")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := drive.Context{UserID: "pan123:user", DriveID: "pan123:user", Token: &model.TokenInfo{AccessToken: "access-token"}}
	store := &pan123MemoryUploadStore{}
	drive.SetUploadSessionStore(store)
	t.Cleanup(func() { drive.SetUploadSessionStore(nil) })

	previous := netx.TestTransportHook
	requestCount := 0
	putCount := 0
	completeCount := 0
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/file/upload_request"):
			requestCount++
			return pan123JSONResponse(`{"code":0,"data":{"Bucket":"bucket","Key":"object","FileId":"7","StorageNode":"node","UploadId":"upload-1"}}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/s3_upload_object/auth"):
			return pan123JSONResponse(`{"code":0,"data":{"presignedUrls":{"1":"https://upload.test/part/1"}}}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/upload_complete/v2"):
			completeCount++
			return pan123JSONResponse(`{"code":0}`, req), nil
		case req.Method == http.MethodPut:
			putCount++
			return pan123JSONResponse(``, req), nil
		}
		return pan123JSONResponse(`{"code":0}`, req), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path,
		ParentFileID:  "pan123_root",
		DriveID:       c.DriveID,
		Name:          "fresh.txt",
		Size:          int64(len(content)),
	}}
	if err := (&Driver{}).UploadOneFile(context.Background(), c, ui); err != nil {
		t.Fatalf("UploadOneFile: %v", err)
	}
	if requestCount != 1 || putCount != 1 || completeCount != 1 {
		t.Fatalf("request=%d put=%d complete=%d, want one each", requestCount, putCount, completeCount)
	}
	if ui.Upload.UploadID != "upload-1" || ui.Upload.FileID != "7" || ui.Upload.DownSize != int64(len(content)) || !ui.Upload.IsCompleted {
		t.Fatalf("upload state = %+v", ui.Upload)
	}
	if !store.cleared {
		t.Fatal("completed fresh upload session was not cleared")
	}
}

func TestPan123UploadInstantReuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reuse.txt")
	content := []byte("already exists")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := drive.Context{UserID: "pan123:user", DriveID: "pan123:user", Token: &model.TokenInfo{AccessToken: "access-token"}}
	store := &pan123MemoryUploadStore{}
	drive.SetUploadSessionStore(store)
	t.Cleanup(func() { drive.SetUploadSessionStore(nil) })

	previous := netx.TestTransportHook
	requestCount := 0
	otherRequestCount := 0
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/file/upload_request") {
			requestCount++
			return pan123JSONResponse(`{"code":0,"data":{"Reuse":true,"FileId":"9"}}`, req), nil
		}
		otherRequestCount++
		return pan123JSONResponse(`{"code":0}`, req), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path,
		ParentFileID:  "pan123_root",
		DriveID:       c.DriveID,
		Name:          "reuse.txt",
		Size:          int64(len(content)),
	}}
	if err := (&Driver{}).UploadOneFile(context.Background(), c, ui); err != nil {
		t.Fatalf("UploadOneFile: %v", err)
	}
	if requestCount != 1 || otherRequestCount != 0 {
		t.Fatalf("request=%d other=%d, want one upload_request and no transfer calls", requestCount, otherRequestCount)
	}
	if ui.Upload.FileID != "9" || ui.Upload.DownSize != int64(len(content)) || !ui.Upload.IsCompleted {
		t.Fatalf("instant upload state = %+v", ui.Upload)
	}
	if !store.cleared {
		t.Fatal("instant upload session was not cleared")
	}
}

func TestPan123UploadRejectsMissingTargetFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })
	for _, data := range []string{`{}`, `{"Reuse":true}`, `{"Reuse":true,"FileId":0}`, `{"Key":"object","UploadId":"upload-1"}`} {
		t.Run(data, func(t *testing.T) {
			netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.Path, "/file/upload_request") {
					t.Errorf("unexpected request after invalid initialization: %s", req.URL.Path)
				}
				return pan123JSONResponse(`{"code":0,"data":`+data+`}`, req), nil
			})
			ui := &model.UploadingUI{Info: model.UploadInfo{LocalFilePath: path, Name: "missing.txt"}}
			c := drive.Context{Token: &model.TokenInfo{AccessToken: "test-token"}}
			err := (&Driver{}).UploadOneFile(context.Background(), c, ui)
			if err == nil || ui.Upload.IsCompleted {
				t.Fatalf("missing destination accepted: error=%v completed=%v", err, ui.Upload.IsCompleted)
			}
		})
	}
}

func TestPan123UploadRequestUsesConflictPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.txt")
	content := []byte("conflict policy")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := drive.Context{UserID: "pan123:policy-user", DriveID: "pan123:policy-drive", Token: &model.TokenInfo{AccessToken: "access-token"}}
	cases := []struct {
		policy string
		want   float64
	}{
		{policy: "", want: 2},
		{policy: "overwrite", want: 2},
		{policy: "rename", want: 1},
		{policy: "refuse", want: 1},
		{policy: "skip", want: 1},
	}
	for _, tc := range cases {
		name := tc.policy
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			previous := netx.TestTransportHook
			var request map[string]any
			var requestErr error
			netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/file/upload_request") {
					raw, err := io.ReadAll(req.Body)
					if err != nil {
						requestErr = err
					} else {
						requestErr = json.Unmarshal(raw, &request)
					}
					return pan123JSONResponse(`{"code":0,"data":{"Reuse":true,"FileId":"9"}}`, req), nil
				}
				return pan123JSONResponse(`{"code":0}`, req), nil
			})
			t.Cleanup(func() { netx.TestTransportHook = previous })

			ui := &model.UploadingUI{Info: model.UploadInfo{
				LocalFilePath:  path,
				ParentFileID:   "pan123_root",
				DriveID:        c.DriveID,
				Name:           "policy.txt",
				ConflictPolicy: tc.policy,
			}}
			if err := (&Driver{}).UploadOneFile(context.Background(), c, ui); err != nil {
				t.Fatalf("UploadOneFile: %v", err)
			}
			if requestErr != nil {
				t.Fatalf("decode upload_request body: %v", requestErr)
			}
			if got, ok := request["duplicate"].(float64); !ok || got != tc.want {
				t.Fatalf("duplicate = %#v, want %v; body=%+v", request["duplicate"], tc.want, request)
			}
		})
	}
}

func TestPan123UploadResignsAfter403(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expired.txt")
	content := []byte("expired presign")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := drive.Context{UserID: "pan123:user", DriveID: "pan123:user", Token: &model.TokenInfo{AccessToken: "access-token"}}
	store := &pan123MemoryUploadStore{}
	drive.SetUploadSessionStore(store)
	t.Cleanup(func() { drive.SetUploadSessionStore(nil) })

	previous := netx.TestTransportHook
	presignCount := 0
	putCount := 0
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/file/upload_request"):
			return pan123JSONResponse(`{"code":0,"data":{"Bucket":"bucket","Key":"object","FileId":"7","StorageNode":"node","UploadId":"upload-1"}}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/s3_upload_object/auth"):
			presignCount++
			return pan123JSONResponse(fmt.Sprintf(`{"code":0,"data":{"presignedUrls":{"1":"https://upload.test/part/%d"}}}`, presignCount), req), nil
		case req.Method == http.MethodPut:
			putCount++
			if putCount == 1 {
				response := pan123JSONResponse(``, req)
				response.StatusCode = http.StatusForbidden
				return response, nil
			}
			return pan123JSONResponse(``, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/upload_complete/v2"):
			return pan123JSONResponse(`{"code":0}`, req), nil
		}
		return pan123JSONResponse(`{"code":0}`, req), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path,
		ParentFileID:  "pan123_root",
		DriveID:       c.DriveID,
		Name:          "expired.txt",
		Size:          int64(len(content)),
	}}
	if err := (&Driver{}).UploadOneFile(context.Background(), c, ui); err != nil {
		t.Fatalf("UploadOneFile: %v", err)
	}
	if presignCount != 2 || putCount != 2 {
		t.Fatalf("presign=%d put=%d, want two each", presignCount, putCount)
	}
	if !ui.Upload.IsCompleted || !store.cleared {
		t.Fatalf("403 retry upload state = %+v cleared=%v", ui.Upload, store.cleared)
	}
}

func TestPan123UploadCompleteFailureKeepsSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "complete-failure.txt")
	content := []byte("complete failure")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	c := drive.Context{UserID: "pan123:user", DriveID: "pan123:user", Token: &model.TokenInfo{AccessToken: "access-token"}}
	store := &pan123MemoryUploadStore{}
	drive.SetUploadSessionStore(store)
	t.Cleanup(func() { drive.SetUploadSessionStore(nil) })

	previous := netx.TestTransportHook
	completeV2Count := 0
	completeV1Count := 0
	netx.TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/file/upload_request"):
			return pan123JSONResponse(`{"code":0,"data":{"Bucket":"bucket","Key":"object","FileId":"7","StorageNode":"node","UploadId":"upload-1"}}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/s3_upload_object/auth"):
			return pan123JSONResponse(`{"code":0,"data":{"presignedUrls":{"1":"https://upload.test/part/1"}}}`, req), nil
		case req.Method == http.MethodPut:
			return pan123JSONResponse(``, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/upload_complete/v2"):
			completeV2Count++
			return pan123JSONResponse(`{"code":500,"message":"complete failed"}`, req), nil
		case strings.HasSuffix(req.URL.Path, "/file/upload_complete"):
			completeV1Count++
			return pan123JSONResponse(`{"code":500,"message":"legacy complete failed"}`, req), nil
		}
		return pan123JSONResponse(`{"code":0}`, req), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = previous })

	ui := &model.UploadingUI{Info: model.UploadInfo{
		LocalFilePath: path,
		ParentFileID:  "pan123_root",
		DriveID:       c.DriveID,
		Name:          "complete-failure.txt",
		Size:          int64(len(content)),
	}}
	err := (&Driver{}).UploadOneFile(context.Background(), c, ui)
	if err == nil || !strings.Contains(err.Error(), "legacy complete failed") {
		t.Fatalf("UploadOneFile error = %v, want legacy completion error", err)
	}
	if completeV2Count != 1 || completeV1Count != 1 {
		t.Fatalf("complete v2=%d v1=%d, want one fallback each", completeV2Count, completeV1Count)
	}
	if ui.Upload.IsCompleted || !ui.Upload.IsFailed {
		t.Fatalf("failed completion state = %+v", ui.Upload)
	}
	if store.cleared {
		t.Fatal("failed completion must keep resumable session")
	}
	if store.sessionID == "" || !reflect.DeepEqual(store.parts, []int{1}) {
		t.Fatalf("saved session = %q parts=%v, want upload-1/[1]", store.sessionID, store.parts)
	}
}

func pan123JSONResponse(body string, req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestPutChunkReturnsHTTPStatusForPresignRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	status, err := putChunk(context.Background(), srv.URL, []byte("chunk"))
	if err != nil {
		t.Fatalf("putChunk returned transport error for HTTP response: %v", err)
	}
	if status != http.StatusForbidden {
		t.Fatalf("putChunk status = %d, want %d", status, http.StatusForbidden)
	}
}

// ---- file list paging data ----

func TestRawList(t *testing.T) {
	items := []any{map[string]any{"FileId": jnum("1")}}
	if got := rawList(map[string]any{"InfoList": items}); len(got) != 1 {
		t.Fatalf("InfoList extraction failed: %v", got)
	}
	if got := rawList(map[string]any{"infoList": items}); len(got) != 1 {
		t.Fatalf("infoList extraction failed: %v", got)
	}
	if got := rawList(map[string]any{}); got != nil {
		t.Fatalf("missing list must be nil, got %v", got)
	}
	// next sentinel detection uses data.Next in the paging loop; verify pick
	if asString(pick(map[string]any{"Next": "-1"}, "Next", "next")) != "-1" {
		t.Fatal("Next extraction failed")
	}
}
