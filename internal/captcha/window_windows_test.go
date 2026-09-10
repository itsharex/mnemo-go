package captcha

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNativeReportResponseBody(t *testing.T) {
	for _, status := range []int32{200, 401} {
		t.Run(strconv.Itoa(int(status)), func(t *testing.T) {
			results := make(chan string, 1)
			s, err := Start(func(_ Session, token string) { results <- token })
			if err != nil {
				t.Fatal(err)
			}
			defer Close()
			token := strings.Repeat("report-token", 4)
			body := bytes.NewReader([]byte(`{"data":{"captcha_token":"` + token + `"}}`))
			unknown := nativeIUnknownVtbl{Release: nativeComProc(windows.NewCallback(func(uintptr) uintptr { return 1 }))}
			stream := nativeIStream{Vtbl: &nativeStreamVtbl{nativeIUnknownVtbl: unknown}}
			stream.Vtbl.Read = nativeComProc(windows.NewCallback(func(_ uintptr, dst *byte, length uint32, read *uint32) uintptr {
				n, _ := body.Read(unsafe.Slice(dst, int(length)))
				*read = uint32(n)
				if n < int(length) {
					return 1
				}
				return 0
			}))
			request := nativeRequest{Vtbl: &nativeRequestVtbl{nativeIUnknownVtbl: unknown}}
			request.Vtbl.GetUri = nativeComProc(windows.NewCallback(func(_ uintptr, out **uint16) uintptr {
				u := windows.StringToUTF16Ptr("https://user.mypikpak.com/credit/v1/report")
				hr, _, _ := windows.NewLazySystemDLL("shlwapi.dll").NewProc("SHStrDupW").Call(uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(out)))
				return hr
			}))
			response := nativeResponse{Vtbl: &nativeResponseVtbl{nativeIUnknownVtbl: unknown}}
			response.Vtbl.GetStatusCode = nativeComProc(windows.NewCallback(func(_ uintptr, out *int32) uintptr { *out = status; return 0 }))
			type callbackVtbl struct {
				nativeIUnknownVtbl
				Invoke nativeComProc
			}
			type callbackObject struct{ Vtbl *callbackVtbl }
			response.Vtbl.GetContent = nativeComProc(windows.NewCallback(func(_ uintptr, cb *callbackObject) uintptr {
				cb.Vtbl.Invoke.Call(uintptr(unsafe.Pointer(cb)), 0, uintptr(unsafe.Pointer(&stream)))
				return 0
			}))
			args := nativeICoreWebView2WebResourceResponseReceivedEventArgs{Vtbl: &nativeResponseArgsVtbl{nativeIUnknownVtbl: unknown}}
			args.Vtbl.GetRequest = nativeComProc(windows.NewCallback(func(_ uintptr, out **nativeRequest) uintptr { *out = &request; return 0 }))
			args.Vtbl.GetResponse = nativeComProc(windows.NewCallback(func(_ uintptr, out **nativeResponse) uintptr { *out = &response; return 0 }))
			w := captchaWindow{ctx: context.Background(), session: *s}
			w.responseReceived(0, unsafe.Pointer(&args))
			select {
			case got := <-results:
				if status != 200 || got != token {
					t.Fatalf("unexpected result %q for status %d", got, status)
				}
			case <-time.After(100 * time.Millisecond):
				if status == 200 {
					t.Fatal("response body token was not delivered")
				}
			}
		})
	}
}

func TestNativeWebViewCallback(t *testing.T) {
	if os.Getenv("MNEMO_TEST_WEBVIEW2") != "1" {
		t.Skip("set MNEMO_TEST_WEBVIEW2=1 to exercise the installed WebView2 Runtime")
	}
	results := make(chan string, 1)
	s, err := Start(func(_ Session, token string) { results <- token })
	if err != nil {
		t.Fatal(err)
	}
	defer Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token := strings.Repeat("native-token", 4)
	profile, err := os.MkdirTemp("", "mnemo-captcha-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		for i := 0; i < 50; i++ {
			if os.RemoveAll(profile) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("WebView2 profile still in use: %s", profile)
	}()
	// The hidden real WebView exercises COM setup, response-event registration,
	// navigation interception and fragment delivery without an online account.
	if err := startNativeWindow(ctx, *s, s.CallbackURL+"#captcha_token="+token, profile, false); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		if got != token {
			t.Fatalf("got %q, want final callback token", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("native callback did not arrive")
	}
}
