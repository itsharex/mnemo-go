package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	_ "mnemo-go/internal/drive/providers/webdav"
	"mnemo-go/internal/model"
)

func TestMigrationFailureKeepsPerFileDetail(t *testing.T) {
	job := &Job{ID: "detail", SrcUser: "missing:source", SrcDrive: "source", DstUser: "missing:target", DstDrive: "target", FileIDs: []string{"missing-file"}}
	_ = NewEngine(nil, nil).Run(context.Background(), job)
	item, ok := job.Items["missing-file"]
	if !ok || item.Status != "failed" || item.Error == "" || item.Verification != "unverified" {
		t.Fatalf("missing failed detail: %+v", job.Items)
	}
}

func TestSpoolMigrationPreservesExistingDestination(t *testing.T) {
	var mu sync.Mutex
	files := map[string]string{"/target/report.txt": "existing", "/source/report.txt": "incoming"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, "incoming")
		case "PROPFIND":
			if _, ok := files[r.URL.Path]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>8</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, r.URL.Path)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			files[r.URL.Path] = string(body)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	drive.SetTokenResolver(func(_, _ string) (*model.TokenInfo, error) {
		return &model.TokenInfo{Conn: &model.ConnConfig{Endpoint: server.URL}}, nil
	})
	defer drive.SetTokenResolver(nil)
	job := &Job{SrcUser: "webdav_source", SrcDrive: "webdav:source", DstUser: "webdav_target", DstDrive: "webdav:target"}
	err := NewEngine(nil, nil).spoolMigrate(context.Background(), job, &model.File{FileID: "/source/report.txt", Name: "report.txt", Size: 8}, "/target")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if files["/target/report.txt"] != "existing" || len(files) != 3 {
		t.Fatalf("migration overwrote existing destination: %#v", files)
	}
	for path, body := range files {
		if path != "/target/report.txt" && body != "incoming" {
			t.Fatalf("new target content = %q", body)
		}
	}
}

func TestStreamMigrationStopsWhenUploaderReturnsEarly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 65536))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readerCh := make(chan io.Reader, 1)
	done := make(chan error, 1)
	go func() {
		done <- streamMigration(ctx, &model.DownloadURL{URL: server.URL}, &Job{}, func(r io.Reader) error {
			readerCh <- r
			return nil
		})
	}()
	reader := <-readerCh
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unread download must not be reported as complete")
		}
	case <-time.After(2 * time.Second):
		cancel()
		_ = reader.(io.Closer).Close()
		<-done
		t.Fatal("migration blocked after uploader returned without consuming the stream")
	}
}

func TestStreamMigrationPropagatesDownloadFailureToUploader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "short")
	}))
	defer server.Close()
	var readErr error
	err := streamMigration(context.Background(), &model.DownloadURL{URL: server.URL}, &Job{}, func(r io.Reader) error {
		_, readErr = io.ReadAll(r)
		return readErr
	})
	if err == nil || !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated source must fail the upload: migration=%v upload=%v", err, readErr)
	}
}

func TestStreamMigrationTransfersCompleteBody(t *testing.T) {
	payload := strings.Repeat("migration-content", 8192)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	job := &Job{}
	var body []byte
	err := streamMigration(context.Background(), &model.DownloadURL{URL: server.URL}, job, func(r io.Reader) error {
		var err error
		body, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != payload || job.ProcessedBytes != int64(len(payload)) {
		t.Fatalf("incorrect transfer: received=%d processed=%d want=%d", len(body), job.ProcessedBytes, len(payload))
	}
}

func TestMigrationDownloadRejectsNonFileResponses(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified, http.StatusAccepted, http.StatusPartialContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusPartialContent {
					w.Header().Set("Content-Range", "bytes 5-9/10")
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "wrong")
			}))
			defer server.Close()
			var body strings.Builder
			err := downloadTo(context.Background(), &model.DownloadURL{URL: server.URL}, &body)
			if err == nil || body.Len() != 0 {
				t.Fatalf("invalid response accepted: status=%d err=%v body=%q", status, err, body.String())
			}
		})
	}
}

func TestCommonHashMethod(t *testing.T) {
	cases := []struct {
		name string
		src  []string
		dst  []string
		want string
	}{
		{"both md5", []string{"md5"}, []string{"md5"}, "md5"},
		{"both sha1", []string{"sha1"}, []string{"sha1"}, "sha1"},
		{"no match", []string{"md5"}, []string{"sha1"}, ""},
		{"multi match", []string{"md5", "sha1"}, []string{"sha1", "md5"}, "md5"},
		{"empty src", []string{}, []string{"md5"}, ""},
	}
	for _, c := range cases {
		got := commonHashMethod(c.src, c.dst)
		if got != c.want {
			t.Errorf("%s: commonHashMethod(%v,%v) = %q, want %q", c.name, c.src, c.dst, got, c.want)
		}
	}
}

func TestRapidUploadAllowedExcludesYike(t *testing.T) {
	if rapidUploadAllowed(model.ProviderYike, "pan123") {
		t.Fatal("yike must not be used as a rapid-upload source")
	}
	if rapidUploadAllowed("pan123", model.ProviderYike) {
		t.Fatal("yike must not be used as a rapid-upload target")
	}
	if !rapidUploadAllowed("pan123", "pan189") {
		t.Fatal("compatible providers should remain eligible")
	}
}

func TestEngineCancelUnknown(t *testing.T) {
	e := NewEngine(nil, nil)
	// should not panic on unknown id
	e.Cancel("nonexistent")
}

func TestEngineRunEmptyFileIDs(t *testing.T) {
	e := NewEngine(nil, nil)
	job := &Job{ID: "t1", FileIDs: []string{}}
	err := e.Run(context.Background(), job)
	if err != nil {
		t.Errorf("Run with empty FileIDs: %v", err)
	}
	if job.Status != "completed" {
		t.Errorf("expected completed, got %s", job.Status)
	}
}

func TestEngineRunSkipsPersistedTopLevelCheckpoints(t *testing.T) {
	e := NewEngine(nil, nil)
	job := &Job{
		ID:               "resume-complete",
		FileIDs:          []string{"already-done"},
		CompletedFileIDs: []string{"already-done", "nested-file"},
		Status:           "canceled",
	}
	if err := e.Run(context.Background(), job); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if job.Status != "completed" {
		t.Fatalf("status = %q, want completed", job.Status)
	}
	if job.Processed != 1 || job.Total != 1 {
		t.Fatalf("processed/total = %d/%d, want 1/1", job.Processed, job.Total)
	}
}

func TestRecoveryCheckpointsAreIdempotent(t *testing.T) {
	e := NewEngine(nil, nil)
	job := &Job{ID: "checkpoint"}

	e.markTargetDirectory(job, "source-dir", "target-dir")
	e.markTargetDirectory(job, "source-dir", "target-dir")
	e.markCopied(job, "source-file")
	e.markCopied(job, "source-file")
	e.markCompleted(job, "source-file")
	e.markCompleted(job, "source-file")

	if got := job.TargetDirectoryIDs["source-dir"]; got != "target-dir" {
		t.Fatalf("target directory checkpoint = %q, want target-dir", got)
	}
	if len(job.CompletedFileIDs) != 1 || job.CompletedFileIDs[0] != "source-file" {
		t.Fatalf("completed checkpoints = %#v", job.CompletedFileIDs)
	}
	if len(job.CopiedFileIDs) != 0 {
		t.Fatalf("copied checkpoint must be cleared after source cleanup: %#v", job.CopiedFileIDs)
	}
}

func TestCompletedTopLevelCountIgnoresNestedCheckpoints(t *testing.T) {
	job := &Job{
		FileIDs:          []string{"root-a", "root-b"},
		CompletedFileIDs: []string{"root-a", "nested-a", "nested-b"},
	}
	if got := completedTopLevelCount(job); got != 1 {
		t.Fatalf("completed top-level count = %d, want 1", got)
	}
}

func TestEngineRejectsDuplicateActiveRun(t *testing.T) {
	e := NewEngine(nil, nil)
	_, cancel, registered := e.registerCancel(context.Background(), "active")
	if !registered {
		t.Fatal("first registration must succeed")
	}
	defer func() {
		cancel()
		e.releaseCancel("active")
	}()

	job := &Job{ID: "active"}
	if err := e.Run(context.Background(), job); err == nil {
		t.Fatal("duplicate active run must be rejected")
	}
	if job.Status != "" {
		t.Fatalf("duplicate run changed status to %q", job.Status)
	}
}

func TestValidateEndpointsRejectsSameDrive(t *testing.T) {
	if err := ValidateEndpoints("pikpak_user", "pikpak:drive", "pikpak_user", "pikpak:drive"); err == nil {
		t.Fatal("same source and target drive must be rejected")
	}
	if err := ValidateEndpoints("pikpak_user", "pikpak:drive", "pikpak_other", "pikpak:drive"); err != nil {
		t.Fatalf("different accounts should remain valid: %v", err)
	}
}

func TestEngineRunRejectsSameDriveBeforeChangingState(t *testing.T) {
	e := NewEngine(nil, nil)
	job := &Job{
		ID: "same-drive", SrcUser: "user", SrcDrive: "drive",
		DstUser: "user", DstDrive: "drive", FileIDs: []string{"file"},
	}
	if err := e.Run(context.Background(), job); err == nil {
		t.Fatal("same-drive migration should fail before provider access")
	}
	if job.Status != "" {
		t.Fatalf("job status changed after validation failure: %q", job.Status)
	}
}

func TestJobProcessedBytesTracking(t *testing.T) {
	j := &Job{ID: "t1", Total: 1000}
	j.ProcessedBytes = 500
	if j.ProcessedBytes != 500 {
		t.Error("ProcessedBytes not set")
	}
	j.Failed = 2
	if j.Failed != 2 {
		t.Error("Failed not set")
	}
}

func TestPartialMigrationErrorUnwrapsThroughMigrationError(t *testing.T) {
	err := newMigrationError(partialError("source cleanup failed"), 1)
	if !isPartialError(err) {
		t.Fatal("partial cleanup errors must remain identifiable after wrapping")
	}
	if failureCount(err) != 1 {
		t.Fatalf("failure count = %d, want 1", failureCount(err))
	}
}

func TestPartialDirectoryCopyCannotBeReportedAsTotalFailure(t *testing.T) {
	err := newPartialMigrationError(context.DeadlineExceeded, 3)
	if !isPartialError(err) {
		t.Fatal("a created target directory with failed children must be partial")
	}
	if failureCount(err) != 3 {
		t.Fatalf("failure count = %d, want 3", failureCount(err))
	}
	if got := migrationResultStatus(3, 0, isPartialError(err)); got != "partial" {
		t.Fatalf("migration status = %q, want partial", got)
	}
	if got := migrationResultStatus(1, 0, false); got != "failed" {
		t.Fatalf("migration status without copied output = %q, want failed", got)
	}
}
