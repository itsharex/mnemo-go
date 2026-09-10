package s3

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

type countingRoundTripper struct {
	mu      sync.Mutex
	methods []string
}

type uploadRoundTripper func(*http.Request) (*http.Response, error)

func (f uploadRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMultipartUploadPreservesSDKBodyAndHonorsLimit(t *testing.T) {
	old := TransportOverride
	netx.SetGlobalUploadRate(1000)
	t.Cleanup(func() { TransportOverride = old; netx.SetGlobalUploadRate(0) })
	puts := 0
	TransportOverride = uploadRoundTripper(func(r *http.Request) (*http.Response, error) {
		body := ""
		header := make(http.Header)
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			body = `<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>file</Key><UploadId>session</UploadId></InitiateMultipartUploadResult>`
		case r.Method == http.MethodPut:
			puts++
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			if len(b) < 300 || r.ContentLength < 300 {
				t.Errorf("SDK upload body/length: %d/%d", len(b), r.ContentLength)
			}
			header.Set("ETag", `"part"`)
		case r.Method == http.MethodPost:
			body = `<CompleteMultipartUploadResult><Bucket>bucket</Bucket><Key>file</Key><ETag>etag</ETag></CompleteMultipartUploadResult>`
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, make([]byte, 300), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cc, err := connOf(drive.Context{Token: &model.TokenInfo{Conn: testS3Config()}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := uploadMultipart(context.Background(), cc, "file", f, &model.UploadingUI{Info: model.UploadInfo{Size: 300}}); err != nil {
		t.Fatal(err)
	}
	if puts != 1 || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("SDK upload bypassed limit: puts=%d elapsed=%v", puts, time.Since(start))
	}
}

func (rt *countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.methods = append(rt.methods, req.Method)
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func (rt *countingRoundTripper) snapshot() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.methods...)
}

func testS3Config() *model.ConnConfig {
	forcePathStyle := true
	return &model.ConnConfig{
		Endpoint:       "http://s3.test",
		Username:       "access-key",
		Password:       "secret-key",
		Bucket:         "bucket",
		Region:         "us-east-1",
		BasePath:       "mount/root",
		ForcePathStyle: &forcePathStyle,
	}
}

func TestValidateConnectionUsesOneRequestOnSuccess(t *testing.T) {
	transport := &countingRoundTripper{}
	previous := TransportOverride
	TransportOverride = transport
	defer func() { TransportOverride = previous }()

	cfg := testS3Config()
	cfg.BasePath = ""
	if err := (&Driver{}).ValidateConnection(context.Background(), cfg); err != nil {
		t.Fatalf("ValidateConnection: %v", err)
	}
	methods := transport.snapshot()
	if len(methods) != 1 || methods[0] != http.MethodHead {
		t.Fatalf("login validation methods = %#v, want one HEAD request", methods)
	}
}

func TestValidateWriteConnectionUsesPutAndDelete(t *testing.T) {
	transport := &countingRoundTripper{}
	previous := TransportOverride
	TransportOverride = transport
	defer func() { TransportOverride = previous }()

	if err := (&Driver{}).ValidateWriteConnection(context.Background(), testS3Config()); err != nil {
		t.Fatalf("ValidateWriteConnection: %v", err)
	}
	methods := transport.snapshot()
	if len(methods) != 2 || methods[0] != http.MethodPut || methods[1] != http.MethodDelete {
		t.Fatalf("write validation methods = %#v, want PUT then DELETE", methods)
	}
}

func TestWriteProbeKeyKeepsConfiguredPrefix(t *testing.T) {
	key, err := writeProbeKey("mount/root/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "mount/root/.mnemo-connection-check/") {
		t.Fatalf("probe key = %q, missing configured prefix", key)
	}
	if strings.Contains(key, "..") {
		t.Fatalf("probe key contains traversal segment: %q", key)
	}
}

func TestCreateShareBuildsTemporaryPresignedLink(t *testing.T) {
	transport := &countingRoundTripper{}
	previous := TransportOverride
	TransportOverride = transport
	t.Cleanup(func() { TransportOverride = previous })

	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "s3:user", DriveID: "s3:user", Token: &model.TokenInfo{Conn: testS3Config()},
	}, drive.ShareParams{FileIDs: []string{"/folder/file.txt"}, ShareName: "测试文件", Expiration: "7"})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	parsed, err := url.Parse(item.ShareURL)
	if err != nil {
		t.Fatalf("share URL = %q: %v", item.ShareURL, err)
	}
	if parsed.Query().Get("X-Amz-Expires") != "604800" {
		t.Fatalf("presigned expiration = %q, want 604800", parsed.Query().Get("X-Amz-Expires"))
	}
	if item.SharePolicy != "presigned" || item.FileID != "/folder/file.txt" || item.SharePwd != "" || item.Expiration == "" {
		t.Fatalf("share = %+v", item)
	}
	methods := transport.snapshot()
	if len(methods) != 1 || methods[0] != http.MethodHead {
		t.Fatalf("share methods = %#v, want one HEAD", methods)
	}
}

func TestCreateShareRejectsUnsupportedS3Options(t *testing.T) {
	c := drive.Context{Token: &model.TokenInfo{Conn: testS3Config()}}
	if _, err := (&Driver{}).CreateShare(t.Context(), c, drive.ShareParams{FileIDs: []string{"/file.txt"}, Expiration: "30"}); err == nil {
		t.Fatal("30-day S3 link must be rejected")
	}
	if _, err := (&Driver{}).CreateShare(t.Context(), c, drive.ShareParams{FileIDs: []string{"/file.txt"}, Password: "secret"}); err == nil {
		t.Fatal("password-protected S3 link must be rejected")
	}
}

func TestTemporaryS3ShareDoesNotAdvertiseHistory(t *testing.T) {
	if (&Driver{}).Capabilities().ShareHistory {
		t.Fatal("temporary presigned URLs must not be advertised as persistent share history")
	}
}
