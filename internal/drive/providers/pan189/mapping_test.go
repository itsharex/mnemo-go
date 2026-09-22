package pan189

import (
	"encoding/json"
	"testing"
	"time"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
)

func TestToFolderID(t *testing.T) {
	cases := map[string]string{
		"":          Pan189DefaultFolder,
		PAN189Root:  Pan189DefaultFolder,
		"root":      Pan189DefaultFolder,
		"/":         Pan189DefaultFolder,
		"-11":       "-11",
		"123456789": "123456789",
		"  root  ":  Pan189DefaultFolder,
	}
	for in, want := range cases {
		if got := toFolderID(in); got != want {
			t.Errorf("toFolderID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayParent(t *testing.T) {
	if got := displayParent(Pan189DefaultFolder); got != PAN189PersonalRoot {
		t.Errorf("displayParent(-11) = %q, want %q", got, PAN189PersonalRoot)
	}
	if got := displayParent("abc"); got != pan189FileID(spacePersonal, "abc") {
		t.Errorf("displayParent(abc) = %q", got)
	}
}

func TestMapFile(t *testing.T) {
	// root listing: parent -11 must surface as pan189_root
	f := mapFile(pan189File{
		ID: "f1", Name: "a.txt", Size: 1024, MD5: "D41D8CD98F00B204E9800998ECF8427E",
		LastOpTime: "2024-01-02 15:04:05", CreateDate: "2024-01-01 00:00:00",
		SmallURL: "https://img/s.png", LargeURL: "https://img/l.png",
	}, "pan189:uid", spacePersonal, "-11")
	if f.FileID != pan189FileID(spacePersonal, "f1") || f.Name != "a.txt" {
		t.Fatalf("basic mapping wrong: %+v", f)
	}
	if f.ParentFileID != PAN189PersonalRoot {
		t.Fatalf("parent should be pan189_personal_root, got %q", f.ParentFileID)
	}
	if f.IsDir {
		t.Fatal("a.txt should be a file")
	}
	if f.Size != 1024 {
		t.Fatalf("size = %d", f.Size)
	}
	if f.ContentHash != "D41D8CD98F00B204E9800998ECF8427E" || f.ContentHashName != "md5" {
		t.Fatalf("md5 hash mapping wrong: %q %q", f.ContentHash, f.ContentHashName)
	}
	if f.Thumbnail != "https://img/s.png" {
		t.Fatalf("thumbnail = %q", f.Thumbnail)
	}
	wantTime := time.Date(2024, 1, 2, 15, 4, 5, 0, time.FixedZone("+0800", 8*3600)).Unix()
	if f.Time != wantTime {
		t.Fatalf("time = %d, want %d", f.Time, wantTime)
	}
	if f.DriveID != "pan189:uid" {
		t.Fatalf("drive id = %q", f.DriveID)
	}

	// folder in a nested directory keeps the real parent
	dir := mapFile(pan189File{ID: "d1", Name: "folder", IsFolder: true, LastOpTime: "2024-03-04 05:06:07"}, "pan189:uid", spacePersonal, "p1")
	if !dir.IsDir || dir.ParentFileID != pan189FileID(spacePersonal, "p1") {
		t.Fatalf("folder mapping wrong: %+v", dir)
	}
	if dir.ContentHash != "" {
		t.Fatal("folder must not carry a content hash")
	}
}

func TestMapFileThumbnailFallback(t *testing.T) {
	f := mapFile(pan189File{ID: "f1", Name: "x.jpg", LargeURL: "https://img/l.png"}, "pan189:uid", spacePersonal, "p1")
	if f.Thumbnail != "https://img/l.png" {
		t.Fatalf("thumbnail fallback = %q", f.Thumbnail)
	}
}

func TestParseTime(t *testing.T) {
	// "2006-01-02 15:04:05" interpreted as +08:00
	want := time.Date(2024, 6, 1, 12, 0, 0, 0, time.FixedZone("+0800", 8*3600)).Unix()
	if got := parseTime("2024-06-01 12:00:00", ""); got != want {
		t.Fatalf("parseTime = %d, want %d", got, want)
	}
	// RFC3339 with explicit offset wins over the +08 default
	wantUTC := time.Date(2024, 6, 1, 4, 0, 0, 0, time.UTC).Unix()
	if got := parseTime("2024-06-01T04:00:00Z", ""); got != wantUTC {
		t.Fatalf("parseTime rfc3339 = %d, want %d", got, wantUTC)
	}
	// empty → now (legacy fallback)
	if got := parseTime("", ""); got == 0 || got > time.Now().Unix()+1 {
		t.Fatalf("parseTime empty = %d", got)
	}
	// lastOpTime preferred over createDate
	if got := parseTime("2024-01-02 00:00:00", "2024-01-01 00:00:00"); got != time.Date(2024, 1, 2, 0, 0, 0, 0, time.FixedZone("+0800", 8*3600)).Unix() {
		t.Fatalf("parseTime preference wrong: %d", got)
	}
}

func TestPaginationMarkers(t *testing.T) {
	if got := pageMarker(""); got != 1 {
		t.Fatalf("pageMarker('') = %d", got)
	}
	if got := pageMarker("abc"); got != 1 {
		t.Fatalf("pageMarker('abc') = %d", got)
	}
	if got := pageMarker("3"); got != 3 {
		t.Fatalf("pageMarker('3') = %d", got)
	}
	if got := markerNext(1, false); got != "2" {
		t.Fatalf("markerNext not done = %q", got)
	}
	if got := markerNext(7, true); got != "" {
		t.Fatalf("markerNext done = %q", got)
	}
}

func TestRawIDString(t *testing.T) {
	if got := rawIDString(json.RawMessage(`"abc"`)); got != "abc" {
		t.Fatalf("rawIDString string = %q", got)
	}
	if got := rawIDString(json.RawMessage(`123456`)); got != "123456" {
		t.Fatalf("rawIDString number = %q", got)
	}
	if got := rawIDString(json.RawMessage(`null`)); got != "" {
		t.Fatalf("rawIDString null = %q", got)
	}
	if got := rawIDString(nil); got != "" {
		t.Fatalf("rawIDString nil = %q", got)
	}
}

func TestParseSession(t *testing.T) {
	if ParseSession("") != nil {
		t.Fatal("empty session must parse to nil")
	}
	if ParseSession("not-json") != nil {
		t.Fatal("invalid json must parse to nil")
	}
	s := &Session{SessionKey: "k", SessionSecret: "s", Username: "u", Password: "p", CloudType: CloudFamily, FamilyID: "9"}
	raw := mustJSON(s)
	got := ParseSession(raw)
	if got == nil {
		t.Fatal("round-trip session parse failed")
	}
	if got.SessionKey != "k" || got.SessionSecret != "s" || got.Username != "u" || got.CloudType != CloudFamily || got.FamilyID != "9" {
		t.Fatalf("session fields wrong: %+v", got)
	}
	// missing secret → invalid
	if ParseSession(`{"sessionKey":"k"}`) != nil {
		t.Fatal("session without secret must be invalid")
	}
}

func TestAttachLoginCredentials(t *testing.T) {
	session := attachLoginCredentials(&Session{SessionKey: "k", SessionSecret: "s"}, "  user@example.com  ", "password")
	if session.Username != "user@example.com" || session.Password != "password" {
		t.Fatalf("login credentials were not attached: %+v", session)
	}
	parsed := ParseSession(mustJSON(session))
	if parsed == nil || parsed.Username != "user@example.com" || parsed.Password != "password" {
		t.Fatalf("login credentials were not persisted: %+v", parsed)
	}
}

func TestPan189UploadSessionState(t *testing.T) {
	c := drive.Context{UserID: "pan189:user", DriveID: "pan189:user"}
	first := pan189UploadSessionKey(c, "-11", "movie.mp4", 100, "abc")
	second := pan189UploadSessionKey(c, "-11", "movie.mp4", 100, "def")
	if first == second {
		t.Fatal("upload session key must include the file hash")
	}

	id, completed := restorePan189UploadState(" upload-1 ", []int{0, 1, 2, 4}, 3)
	if id != "upload-1" || !completed[1] || !completed[2] || completed[4] || completed[0] {
		t.Fatalf("restored upload state is invalid: id=%q parts=%v", id, completed)
	}
}

func TestSessionOf(t *testing.T) {
	tok := model.TokenInfo{AccessToken: "skey", RefreshToken: mustJSON(sessionForTest())}
	s, err := sessionOf(&tok)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionKey != "skey" {
		t.Fatalf("session key = %q", s.SessionKey)
	}
	// AccessToken wins over stored sessionKey (legacy sync rule)
	tok2 := model.TokenInfo{AccessToken: "newkey", RefreshToken: mustJSON(sessionForTest())}
	s2, err := sessionOf(&tok2)
	if err != nil {
		t.Fatal(err)
	}
	if s2.SessionKey != "newkey" {
		t.Fatalf("access token should override session key, got %q", s2.SessionKey)
	}
}

func TestCloudInfo(t *testing.T) {
	if isFamily, _ := cloudInfo(&Session{CloudType: CloudPersonal}); isFamily {
		t.Fatal("personal must not be family")
	}
	if isFamily, id := cloudInfo(&Session{CloudType: CloudFamily, FamilyID: "42"}); !isFamily || id != "42" {
		t.Fatalf("family = %v %q", isFamily, id)
	}
	if isFamily, _ := cloudInfo(nil); isFamily {
		t.Fatal("nil session must not be family")
	}
}

func TestParsePan189Quota(t *testing.T) {
	for _, tc := range []struct {
		name      string
		payload   string
		wantUsed  int64
		wantTotal int64
		wantOK    bool
	}{
		{
			name:      "string values",
			payload:   `{"userSizeInfo":{"usedSize":"123","cloudCapacityInfo":{"totalSize":"456"}}}`,
			wantUsed:  123,
			wantTotal: 456,
			wantOK:    true,
		},
		{
			name:      "numeric values and zero used",
			payload:   `{"userSizeInfo":{"usedSize":0,"cloudCapacityInfo":{"totalSize":1024}}}`,
			wantUsed:  0,
			wantTotal: 1024,
			wantOK:    true,
		},
		{
			name:      "used size is clamped",
			payload:   `{"userSizeInfo":{"usedSize":"200","cloudCapacityInfo":{"totalSize":"100"}}}`,
			wantUsed:  100,
			wantTotal: 100,
			wantOK:    true,
		},
		{
			name:    "invalid value",
			payload: `{"userSizeInfo":{"usedSize":"bad","cloudCapacityInfo":{"totalSize":"100"}}}`,
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, total, ok := parsePan189Quota([]byte(tc.payload))
			if used != tc.wantUsed || total != tc.wantTotal || ok != tc.wantOK {
				t.Fatalf("parsePan189Quota() = (%d, %d, %v), want (%d, %d, %v)", used, total, ok, tc.wantUsed, tc.wantTotal, tc.wantOK)
			}
		})
	}
}

func TestApplyPan189QuotaKeepsPreviousSnapshotOnInvalidResponse(t *testing.T) {
	token := &model.TokenInfo{UsedSize: 20, TotalSize: 100, FreeSize: 80}
	applyPan189Quota(token, 0, 0, false)
	if token.UsedSize != 20 || token.TotalSize != 100 || token.FreeSize != 80 {
		t.Fatalf("invalid quota cleared cache: %+v", token)
	}
	applyPan189Quota(token, 0, 100, true)
	if token.UsedSize != 0 || token.TotalSize != 100 || token.FreeSize != 100 {
		t.Fatalf("zero used quota was not applied: %+v", token)
	}
}

func sessionForTest() *Session {
	return &Session{SessionKey: "skey", SessionSecret: "ssecret", Username: "u", Password: "p"}
}
