package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"mnemo-go/internal/drive"
	"mnemo-go/internal/model"
	"mnemo-go/internal/netx"
)

type onedriveRoundTripper func(*http.Request) (*http.Response, error)

func (f onedriveRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func onedriveResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestFavoritesRejectUnknownAccountTypeAndMalformedList(t *testing.T) {
	d := &Driver{}
	ctx := drive.Context{DriveID: "local", Token: &model.TokenInfo{AccessToken: "token", ProviderDriveType: "unexpected"}}
	if supported, err := d.SupportsRemoteFavorites(context.Background(), ctx); err == nil || supported {
		t.Fatal("unknown account type treated as business")
	}
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	netx.TestTransportHook = onedriveRoundTripper(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		if r.URL.Path == "/v1.0/me/drive" {
			body = `{"id":"drive"}`
		}
		return onedriveResponse(r, 200, body), nil
	})
	ctx.Token.ProviderDriveType = "business"
	if _, err := d.ListFavorites(context.Background(), ctx); err == nil {
		t.Fatal("malformed following list accepted as empty snapshot")
	}
}

func TestFavoritesPaginateWithinCurrentDriveAndFollowUnfollow(t *testing.T) {
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	netx.TestTransportHook = onedriveRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1.0/me/drive":
			return onedriveResponse(r, 200, `{"id":"own-drive","driveType":"business"}`), nil
		case "/v1.0/me/drive/following":
			if r.URL.Query().Get("skiptoken") == "next" {
				return onedriveResponse(r, 200, `{"value":[{"id":"folder","name":"Folder","folder":{},"parentReference":{"id":"root","driveId":"own-drive"}}]}`), nil
			}
			return onedriveResponse(r, 200, `{"value":[{"id":"file","name":"file.jpg","size":42,"parentReference":{"id":"parent","driveId":"own-drive"}},{"id":"foreign","name":"foreign.jpg","parentReference":{"driveId":"foreign-drive"}}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/drive/following?skiptoken=next"}`), nil
		case "/v1.0/me/drive/items/file/follow":
			if r.Method != http.MethodPost {
				t.Fatal("follow method")
			}
			return onedriveResponse(r, 200, `{"id":"file"}`), nil
		case "/v1.0/me/drive/items/file/unfollow":
			if r.Method != http.MethodPost {
				t.Fatal("unfollow method")
			}
			return onedriveResponse(r, 204, ""), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})
	d := &Driver{}
	c := drive.Context{DriveID: "mounted", Token: &model.TokenInfo{AccessToken: "token"}}
	if supported, err := d.SupportsRemoteFavorites(context.Background(), c); err != nil || !supported || c.Token.ProviderDriveType != "business" {
		t.Fatalf("support=%v, %v", supported, err)
	}
	files, err := d.ListFavorites(context.Background(), c)
	if err != nil || len(files) != 2 || files[0].FileID != "file" || files[0].ParentFileID != "parent" || files[0].Size != 42 || !files[0].Starred || !files[1].IsDir {
		t.Fatalf("favorites=%+v, %v", files, err)
	}
	for _, favorite := range []bool{true, false} {
		ids, err := d.Favorite(context.Background(), c, []string{"file"}, favorite)
		if err != nil || len(ids) != 1 || ids[0] != "file" {
			t.Fatalf("favorite=%v %v", ids, err)
		}
	}
}

func TestFavoritesRejectPartialPagesAndUnconfirmedFollow(t *testing.T) {
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	for _, page := range []string{
		`{"value":[],"@odata.nextLink":"/me/drive/following"}`,
		`{"value":[],"@odata.nextLink":"https://untrusted.invalid/following"}`,
		`{"value":[{"id":"file"}]}`,
	} {
		t.Run(page, func(t *testing.T) {
			netx.TestTransportHook = onedriveRoundTripper(func(r *http.Request) (*http.Response, error) {
				body := page
				if r.URL.Host != "graph.microsoft.com" {
					t.Fatal("token sent to foreign server")
				}
				if r.URL.Path == "/v1.0/me/drive" {
					body = `{"id":"own-drive"}`
				}
				return onedriveResponse(r, 200, body), nil
			})
			if _, err := (&Driver{}).ListFavorites(context.Background(), drive.Context{Token: &model.TokenInfo{AccessToken: "token"}}); err == nil {
				t.Fatal("incomplete snapshot accepted")
			}
		})
	}
	netx.TestTransportHook = onedriveRoundTripper(func(r *http.Request) (*http.Response, error) { return onedriveResponse(r, 200, `{}`), nil })
	ids, err := (&Driver{}).Favorite(context.Background(), drive.Context{Token: &model.TokenInfo{AccessToken: "token"}}, []string{"file"}, true)
	if err == nil || len(ids) != 0 {
		t.Fatal("unconfirmed follow accepted")
	}
}

func TestCreateShareUsesMicrosoftGraphCreateLink(t *testing.T) {
	previous := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = previous })

	netx.TestTransportHook = onedriveRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "graph.microsoft.com" || req.URL.Path != "/v1.0/me/drive/items/file-1/createLink" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer access-token" {
			return nil, fmt.Errorf("authorization = %q", req.Header.Get("Authorization"))
		}
		var body struct {
			Type       string `json:"type"`
			Scope      string `json:"scope"`
			Password   string `json:"password"`
			Expiration string `json:"expirationDateTime"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body.Type != "view" || body.Scope != "anonymous" || body.Password != "p4ss" || body.Expiration != "2030-01-01T00:00:00Z" {
			return nil, fmt.Errorf("share body = %+v", body)
		}
		return onedriveResponse(req, http.StatusOK, `{"id":"permission-1","expirationDateTime":"2030-01-01T00:00:00Z","link":{"type":"view","scope":"anonymous","webUrl":"https://1drv.ms/u/share-1"}}`), nil
	})

	item, err := (&Driver{}).CreateShare(context.Background(), drive.Context{
		UserID: "onedrive:user", DriveID: "onedrive:user", Token: &model.TokenInfo{AccessToken: "access-token"},
	}, drive.ShareParams{FileIDs: []string{"file-1"}, Expiration: "2030-01-01T00:00:00Z", Password: "p4ss"})
	if err != nil {
		t.Fatalf("CreateShare() error = %v", err)
	}
	if item.ShareID != "permission-1" || item.ShareURL != "https://1drv.ms/u/share-1" || item.FileID != "file-1" || item.AccountID != "onedrive:user" {
		t.Fatalf("share = %+v", item)
	}
}

func TestOneDriveCancelShareDeletesCreatedPermission(t *testing.T) {
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })
	netx.TestTransportHook = onedriveRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodDelete || req.URL.Host != "graph.microsoft.com" || req.URL.Path != "/v1.0/me/drive/items/file-1/permissions/permission-1" {
			return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer access-token" {
			return nil, fmt.Errorf("authorization = %q", req.Header.Get("Authorization"))
		}
		return onedriveResponse(req, http.StatusNoContent, ""), nil
	})

	err := (&Driver{}).CancelShare(context.Background(), drive.Context{Token: &model.TokenInfo{AccessToken: "access-token"}}, model.ShareHistoryEntry{FileID: "file-1", ShareID: "permission-1"})
	if err != nil {
		t.Fatalf("CancelShare() error = %v", err)
	}
}

func TestOneDrivePresignedDownloadURLDoesNotCarryBearerToken(t *testing.T) {
	old := netx.TestTransportHook
	netx.TestTransportHook = onedriveRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1.0/me/drive/items/file-1" {
			return onedriveResponse(r, http.StatusNotFound, `{}`), nil
		}
		return onedriveResponse(r, http.StatusOK, `{"id":"file-1","name":"movie.mp4","size":4,"@microsoft.graph.downloadUrl":"https://download.example/movie"}`), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = old })

	dl, err := (&Driver{}).GetDownloadURL(context.Background(), drive.Context{
		DriveID: "onedrive-drive",
		Token:   &model.TokenInfo{AccessToken: "secret"},
	}, "file-1", 0)
	if err != nil {
		t.Fatalf("GetDownloadURL returned error: %v", err)
	}
	if dl.URL != "https://download.example/movie" || len(dl.Headers) != 0 {
		t.Fatalf("presigned download = %+v", dl)
	}
}

func TestOneDriveContentFallbackKeepsBearerToken(t *testing.T) {
	old := netx.TestTransportHook
	netx.TestTransportHook = onedriveRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1.0/me/drive/items/file-1" {
			return onedriveResponse(r, http.StatusNotFound, `{}`), nil
		}
		return onedriveResponse(r, http.StatusOK, `{"id":"file-1","name":"movie.mp4","size":4}`), nil
	})
	t.Cleanup(func() { netx.TestTransportHook = old })

	dl, err := (&Driver{}).GetDownloadURL(context.Background(), drive.Context{
		DriveID: "onedrive-drive",
		Token:   &model.TokenInfo{AccessToken: "secret"},
	}, "file-1", 0)
	if err != nil {
		t.Fatalf("GetDownloadURL returned error: %v", err)
	}
	if dl.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("content fallback headers = %#v", dl.Headers)
	}
}

func TestOneDriveListRefreshesInvalidAccessTokenWithoutSurfacingInitial401(t *testing.T) {
	old := netx.TestTransportHook
	t.Cleanup(func() { netx.TestTransportHook = old })

	graphCalls := 0
	refreshCalls := 0
	netx.TestTransportHook = onedriveRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "graph.microsoft.com":
			graphCalls++
			if r.Header.Get("Authorization") == "Bearer stale-token" {
				return onedriveResponse(r, http.StatusUnauthorized, `{"error":{"code":"InvalidAuthenticationToken","message":"JWT is not well formed"}}`), nil
			}
			if r.Header.Get("Authorization") != "Bearer refreshed-token" {
				return nil, fmt.Errorf("unexpected graph authorization %q", r.Header.Get("Authorization"))
			}
			return onedriveResponse(r, http.StatusOK, `{"value":[{"id":"file-1","name":"one.txt","size":1,"file":{}}]}`), nil
		case "login.microsoftonline.com":
			refreshCalls++
			if r.Method != http.MethodPost || r.URL.String() != msTokenURL {
				return nil, fmt.Errorf("unexpected refresh request %s %s", r.Method, r.URL)
			}
			return onedriveResponse(r, http.StatusOK, `{"access_token":"refreshed-token","refresh_token":"refresh-new","expires_in":3600,"token_type":"Bearer"}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", r.URL)
		}
	})

	token := &model.TokenInfo{AccessToken: "stale-token", RefreshToken: "refresh-old", DeviceID: "client-id"}
	items, err := (&Driver{}).List(context.Background(), drive.Context{DriveID: "onedrive-drive", Token: token}, RootID, nil)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if graphCalls != 2 || refreshCalls != 1 || len(items) != 1 || items[0].FileID != "file-1" {
		t.Fatalf("calls/items = graph:%d refresh:%d items:%+v", graphCalls, refreshCalls, items)
	}
	if token.AccessToken != "refreshed-token" || token.RefreshToken != "refresh-new" {
		t.Fatalf("rotated token = %+v", token)
	}
}

func TestGraphAuthenticationFailureUsesStructuredError(t *testing.T) {
	authErr := graphError([]byte(`{"error":{"code":"InvalidAuthenticationToken","message":"JWT is not well formed"}}`), http.StatusUnauthorized)
	if !isGraphAuthenticationFailure(fmt.Errorf("list root: %w", authErr)) {
		t.Fatal("structured InvalidAuthenticationToken should allow one refresh retry")
	}

	accessErr := graphError([]byte(`{"error":{"code":"AccessDenied","message":"policy blocked"}}`), http.StatusUnauthorized)
	if isGraphAuthenticationFailure(accessErr) {
		t.Fatal("unrelated Graph 401 must not refresh the token")
	}
	if isGraphAuthenticationFailure(fmt.Errorf("onedrive: http 401: InvalidAuthenticationToken")) {
		t.Fatal("formatted error text alone must not trigger a refresh retry")
	}
}
