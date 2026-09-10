package pikpak

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"mnemo-go/internal/netx"
	"mnemo-go/internal/preview"
	"mnemo-go/internal/store"
)

// Opt-in read-only diagnostic. No filenames, tokens or signed URLs are logged.
func TestPikPakLiveReadOnly(t *testing.T) {
	dir := os.Getenv("MNEMO_PIKPAK_DIAGNOSTIC_ACCOUNT_DIR")
	if dir == "" {
		t.Skip("live diagnostic disabled")
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal("cannot open account store")
	}
	accounts, err := st.ListAccounts()
	if err != nil {
		t.Fatal("cannot decode account store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, account := range accounts {
		if account.Token == nil || account.Token.TokenFrom != "pikpak" {
			continue
		}
		tok := account.Token
		c := newClient(tok.AccessToken, tok.DeviceID, tok.ProviderAccountID)
		t.Logf("PikPak account available; API proxy configured=%t", c.http.Proxy != "")
		folders := []string{""}
		for visited := 0; len(folders) > 0 && visited < 12; visited++ {
			parent := folders[0]
			folders = folders[1:]
			files, _, err := c.ListPage(ctx, parent, "", false)
			if err != nil {
				t.Fatalf("list failed (type %T)", err)
			}
			t.Logf("directory %d: %d items", visited, len(files))
			for _, f := range files {
				if f.Kind == "drive#folder" {
					folders = append(folders, f.ID)
					continue
				}
				if !strings.HasPrefix(f.MimeType, "video/") && !strings.HasSuffix(strings.ToLower(f.Name), ".mp4") && !strings.HasSuffix(strings.ToLower(f.Name), ".mkv") {
					continue
				}
				p, err := c.PlayInfo(ctx, f.ID)
				if err != nil {
					t.Fatalf("playback metadata failed (type %T)", err)
				}
				t.Logf("playback metadata: %d qualities", len(p.Qualities))
				if output := os.Getenv("MNEMO_PIKPAK_DIAGNOSTIC_OUTPUT"); output != "" {
					server, err := preview.NewServer()
					if err != nil {
						t.Fatal("cannot start playback proxy")
					}
					defer server.Close()
					for i := range p.Qualities {
						q := &p.Qualities[i]
						q.URL, err = server.PlaybackURL(preview.PlaybackSource{URL: q.URL, StreamType: q.Type, Filename: "diagnostic.ts"})
						if err != nil {
							t.Fatal("cannot create playback session")
						}
					}
					p.URL = p.Qualities[0].URL
					p.StreamType = p.Qualities[0].Type
					p.CurrentQuality = p.Qualities[0].Value
					data, _ := json.Marshal(p)
					if err := os.WriteFile(output, data, 0600); err != nil {
						t.Fatal("cannot write local browser session")
					}
					defer os.Remove(output)
					t.Log("browser diagnostic proxy ready")
					for {
						select {
						case <-ctx.Done():
							t.Fatal("browser diagnostic timed out")
						case <-time.After(time.Second):
							if _, err := os.Stat(output + ".done"); err == nil {
								os.Remove(output + ".done")
								return
							}
						}
					}
				}
				detail, err := c.Detail(ctx, f.ID)
				if err != nil {
					t.Fatal("download detail failed")
				}
				t.Logf("detail mime=%s phase=%s medias=%d", detail.MimeType, detail.Phase, len(detail.Medias))
				for i, m := range detail.Medias {
					visible := "unset"
					if m.IsVisible != nil {
						if *m.IsVisible {
							visible = "true"
						} else {
							visible = "false"
						}
					}
					t.Logf("media %d: origin=%t visible=%s quota=%t video=%+v link=%t", i, m.IsOrigin, visible, m.NeedMoreQuota, m.Video, m.Link != nil && m.Link.URL != "")
				}
				var cached File
				if err := c.get(ctx, "/drive/v1/files/"+url.PathEscape(f.ID), url.Values{"usage": {"CACHE"}, "_magic": {"2021"}}, &cached); err == nil {
					t.Logf("CACHE detail medias=%d link=%t", len(cached.Medias), originalLink(&cached) != "")
					for i, m := range cached.Medias {
						t.Logf("CACHE media %d origin=%t video=%+v link=%t", i, m.IsOrigin, m.Video, m.Link != nil && m.Link.URL != "")
					}
				} else {
					t.Logf("CACHE failed type=%T", err)
				}
				for i, q := range p.Qualities {
					hc := netx.NewClientWithSystemProxy(20 * time.Second)
					resp, err := hc.Do(ctx, http.MethodGet, q.URL, map[string]string{"Range": "bytes=0-1023"}, nil)
					if err != nil {
						t.Logf("quality %d type=%s: network error (%T)", i, q.Type, err)
						continue
					}
					b, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
					resp.Body.Close()
					t.Logf("quality %d type=%s: status=%d mime=%s bytes=%d readOK=%t", i, q.Type, resp.StatusCode, resp.Header.Get("Content-Type"), len(b), readErr == nil)
				}
				return
			}
		}
		t.Fatal("no video found within bounded diagnostic search")
	}
	t.Fatal("no saved PikPak account")
}
