package captcha

// The isolated WebView has no Wails bindings or injected host objects. COM calls
// and the window message loop stay on one STA thread. Interface layouts and IIDs
// below follow Microsoft's WebView2 SDK (1.0.3179.45, WebView2.h).
import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/combridge"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

type nativeInvoke interface {
	Invoke(uintptr, unsafe.Pointer) uintptr
}
type controllerCallback interface{ nativeInvoke }
type navigationCallback interface{ nativeInvoke }
type popupCallback interface{ nativeInvoke }
type responseCallback interface{ nativeInvoke }
type contentCallback interface{ nativeInvoke }
type invokeFunc func(uintptr, unsafe.Pointer) uintptr

func (f invokeFunc) Invoke(a uintptr, b unsafe.Pointer) uintptr { return f(a, b) }

func registerNativeCallback[T nativeInvoke](iid string) {
	combridge.RegisterVTable[combridge.IUnknown, T](iid, func(this uintptr, a uintptr, b unsafe.Pointer) uintptr {
		return combridge.Resolve[T](this).Invoke(a, b)
	})
}

func init() {
	registerNativeCallback[controllerCallback]("{6c4819f3-c9b7-4260-8127-c9f5bde7f68c}")
	registerNativeCallback[navigationCallback]("{9adbe429-f36d-432b-9ddc-f8881fbd76e3}")
	registerNativeCallback[popupCallback]("{d4c185fe-c81c-4989-97af-2d3fa7ab5651}")
	registerNativeCallback[responseCallback]("{7de9898a-24f5-40c3-a2de-d4f458e69828}")
	registerNativeCallback[contentCallback]("{875738e1-9fa2-40e3-8b74-2e8972dd6fe7}")
	launchWindow = func(ctx context.Context, session Session, rawURL, profile string) error {
		return startNativeWindow(ctx, session, rawURL, profile, true)
	}
}

var (
	captchaUser32    = windows.NewLazySystemDLL("user32.dll")
	captchaKernel32  = windows.NewLazySystemDLL("kernel32.dll")
	captchaOle32     = windows.NewLazySystemDLL("ole32.dll")
	nativeClassOnce  sync.Once
	nativeClassErr   error
	nativeWindows    sync.Map
	nativeClassName  = windows.StringToUTF16Ptr("MnemoPikPakCaptcha")
	nativeWindowProc = windows.NewCallback(captchaWindowProc)
)

const (
	wmClose            = 0x0010
	wmDestroy          = 0x0002
	wmSize             = 0x0005
	wsOverlappedWindow = 0x00CF0000
	wsClipChildren     = 0x02000000
)

type nativeWindowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type nativeMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

type captchaWindow struct {
	ctx        context.Context
	session    Session
	url        string
	hwnd       uintptr
	controller *nativeICoreWebView2Controller
	view       *nativeICoreWebView2
	ready      chan error
	readyOnce  sync.Once
	closed     bool
	visible    bool
}

func startNativeWindow(ctx context.Context, session Session, rawURL, profile string, visible bool) error {
	if err := os.MkdirAll(profile, 0700); err != nil {
		return err
	}
	w := &captchaWindow{ctx: ctx, session: session, url: rawURL, ready: make(chan error, 1), visible: visible}
	go w.run(profile)
	select {
	case err := <-w.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return errors.New("WebView2 初始化超时")
	}
}

func (w *captchaWindow) signal(err error) { w.readyOnce.Do(func() { w.ready <- err }) }

func (w *captchaWindow) run(profile string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := captchaOle32.NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 {
		w.signal(fmt.Errorf("CoInitializeEx: 0x%x", hr))
		return
	}
	defer captchaOle32.NewProc("CoUninitialize").Call()
	instance, _, _ := captchaKernel32.NewProc("GetModuleHandleW").Call(0)
	nativeClassOnce.Do(func() {
		cursor, _, _ := captchaUser32.NewProc("LoadCursorW").Call(0, 32512)
		class := nativeWindowClass{Proc: nativeWindowProc, Instance: instance, Cursor: cursor, Background: 6, ClassName: nativeClassName}
		class.Size = uint32(unsafe.Sizeof(class))
		atom, _, err := captchaUser32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
		if atom == 0 {
			nativeClassErr = err
		}
	})
	if nativeClassErr != nil {
		w.signal(nativeClassErr)
		return
	}
	title := windows.StringToUTF16Ptr("PikPak 安全验证")
	hwnd, _, err := captchaUser32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(nativeClassName)), uintptr(unsafe.Pointer(title)), wsOverlappedWindow|wsClipChildren, 0x80000000, 0x80000000, 520, 720, 0, 0, instance, 0)
	if hwnd == 0 {
		w.signal(err)
		return
	}
	w.hwnd = hwnd
	nativeWindows.Store(hwnd, w)
	defer nativeWindows.Delete(hwnd)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-w.ctx.Done():
			captchaUser32.NewProc("PostMessageW").Call(hwnd, wmClose, 0, 0)
		case <-done:
		}
	}()
	if err := webviewloader.CreateCoreWebView2EnvironmentWithOptions(w, webviewloader.WithUserDataFolder(profile)); err != nil {
		w.signal(err)
		captchaUser32.NewProc("DestroyWindow").Call(hwnd)
	}
	var msg nativeMessage
	for {
		r, _, _ := captchaUser32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		captchaUser32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		captchaUser32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
	w.signal(errors.New("验证窗口已关闭"))
}

func captchaWindowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	if value, ok := nativeWindows.Load(hwnd); ok {
		w := value.(*captchaWindow)
		switch message {
		case wmSize:
			w.resize()
		case wmClose:
			captchaUser32.NewProc("DestroyWindow").Call(hwnd)
			return 0
		case wmDestroy:
			w.closed = true
			if w.controller != nil {
				_ = w.controller.Close()
				releaseNative(unsafe.Pointer(w.controller))
				w.controller = nil
			}
			if w.view != nil {
				releaseNative(unsafe.Pointer(w.view))
				w.view = nil
			}
			captchaUser32.NewProc("PostQuitMessage").Call(0)
			return 0
		}
	}
	r, _, _ := captchaUser32.NewProc("DefWindowProcW").Call(hwnd, uintptr(message), wparam, lparam)
	return r
}

func (w *captchaWindow) resize() {
	if w.controller == nil {
		return
	}
	var bounds nativeRECT
	captchaUser32.NewProc("GetClientRect").Call(w.hwnd, uintptr(unsafe.Pointer(&bounds)))
	_ = w.controller.PutBounds(bounds)
}

func (w *captchaWindow) fail(err error) {
	w.signal(err)
	captchaUser32.NewProc("PostMessageW").Call(w.hwnd, wmClose, 0, 0)
}

func (w *captchaWindow) EnvironmentCompleted(code webviewloader.HRESULT, env *webviewloader.ICoreWebView2Environment) webviewloader.HRESULT {
	if w.closed || w.ctx.Err() != nil {
		return 0
	}
	if code < 0 || env == nil {
		w.fail(fmt.Errorf("WebView2 environment: 0x%x", uint32(code)))
		return 0
	}
	e := (*nativeICoreWebView2Environment)(unsafe.Pointer(env))
	handler := combridge.New[controllerCallback](invokeFunc(w.controllerCreated))
	defer handler.Close()
	hr, _, _ := e.Vtbl.CreateCoreWebView2Controller.Call(uintptr(unsafe.Pointer(e)), w.hwnd, handler.Ref())
	if int32(hr) < 0 {
		w.fail(syscall.Errno(hr))
	}
	return 0
}

// go-webview2 v1.0.22's generated ICoreWebView2_2 table omits its base
// ICoreWebView2 slots. Retain the complete base layout for response events.
type captchaView2Vtbl struct {
	nativeICoreWebView2Vtbl
	AddWebResourceResponseReceived    nativeComProc
	RemoveWebResourceResponseReceived nativeComProc
}
type captchaView2 struct{ Vtbl *captchaView2Vtbl }

func (w *captchaWindow) controllerCreated(code uintptr, result unsafe.Pointer) uintptr {
	if w.closed || w.ctx.Err() != nil {
		if result != nil {
			_ = (*nativeICoreWebView2Controller)(result).Close()
		}
		return 0
	}
	if int32(code) < 0 || result == nil {
		w.fail(fmt.Errorf("WebView2 controller: 0x%x", code))
		return 0
	}
	w.controller = (*nativeICoreWebView2Controller)(result)
	w.controller.AddRef()
	view, err := w.controller.GetCoreWebView2()
	if err != nil {
		w.fail(err)
		return 0
	}
	w.view = view
	if err := w.configure(); err != nil {
		w.fail(err)
		return 0
	}
	w.resize()
	w.controller.Vtbl.PutIsVisible.Call(uintptr(unsafe.Pointer(w.controller)), 1)
	if err := view.Navigate(w.url); err != nil {
		w.fail(err)
		return 0
	}
	if w.visible {
		captchaUser32.NewProc("ShowWindow").Call(w.hwnd, 5)
		captchaUser32.NewProc("SetForegroundWindow").Call(w.hwnd)
	}
	w.signal(nil)
	return 0
}

func (w *captchaWindow) configure() error {
	settings, err := w.view.GetSettings()
	if err != nil {
		return err
	}
	defer releaseNative(unsafe.Pointer(settings))
	// Use BOOL values, not addresses (some generated setters pass &bool).
	for _, setter := range []nativeComProc{settings.Vtbl.PutIsWebMessageEnabled, settings.Vtbl.PutAreHostObjectsAllowed, settings.Vtbl.PutAreDevToolsEnabled, settings.Vtbl.PutAreDefaultContextMenusEnabled} {
		hr, _, _ := setter.Call(uintptr(unsafe.Pointer(settings)), 0)
		if int32(hr) < 0 {
			return syscall.Errno(hr)
		}
	}
	nav := combridge.New[navigationCallback](invokeFunc(func(_ uintptr, ptr unsafe.Pointer) uintptr {
		a := (*nativeICoreWebView2NavigationStartingEventArgs)(ptr)
		raw, err := nativeString(ptr, a.Vtbl.GetUri)
		if err != nil || w.intercept(raw) {
			a.Vtbl.PutCancel.Call(uintptr(ptr), 1)
		}
		return 0
	}))
	defer nav.Close()
	var eventToken int64
	hr, _, _ := w.view.Vtbl.AddNavigationStarting.Call(uintptr(unsafe.Pointer(w.view)), nav.Ref(), uintptr(unsafe.Pointer(&eventToken)))
	if int32(hr) < 0 {
		return syscall.Errno(hr)
	}
	popup := combridge.New[popupCallback](invokeFunc(func(_ uintptr, ptr unsafe.Pointer) uintptr {
		a := (*nativeICoreWebView2NewWindowRequestedEventArgs)(ptr)
		a.Vtbl.PutHandled.Call(uintptr(ptr), 1)
		if raw, err := nativeString(ptr, a.Vtbl.GetUri); err == nil && !w.intercept(raw) {
			_ = w.view.Navigate(raw)
		}
		return 0
	}))
	defer popup.Close()
	hr, _, _ = w.view.Vtbl.AddNewWindowRequested.Call(uintptr(unsafe.Pointer(w.view)), popup.Ref(), uintptr(unsafe.Pointer(&eventToken)))
	if int32(hr) < 0 {
		return syscall.Errno(hr)
	}
	iid := windows.GUID{Data1: 0x9e8f0cf8, Data2: 0xe670, Data3: 0x4b5e, Data4: [8]byte{0xb2, 0xbc, 0x73, 0xe0, 0x61, 0xe3, 0x18, 0x4c}}
	var extended *captchaView2
	hr, _, _ = w.view.Vtbl.QueryInterface.Call(uintptr(unsafe.Pointer(w.view)), uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&extended)))
	if int32(hr) < 0 || extended == nil {
		return errors.New("WebView2 不支持验证响应读取，请更新 WebView2 Runtime")
	}
	defer releaseNative(unsafe.Pointer(extended))
	response := combridge.New[responseCallback](invokeFunc(w.responseReceived))
	defer response.Close()
	hr, _, _ = extended.Vtbl.AddWebResourceResponseReceived.Call(uintptr(unsafe.Pointer(extended)), response.Ref(), uintptr(unsafe.Pointer(&eventToken)))
	if int32(hr) < 0 {
		return syscall.Errno(hr)
	}
	return nil
}

func (w *captchaWindow) intercept(raw string) bool {
	if token, ok := callbackToken(w.session, raw); ok {
		queueCallback(w.session.ID, token)
		return true
	}
	u, err := url.Parse(raw)
	return err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil
}

func (w *captchaWindow) responseReceived(_ uintptr, ptr unsafe.Pointer) uintptr {
	if w.closed || w.ctx.Err() != nil {
		return 0
	}
	args := (*nativeICoreWebView2WebResourceResponseReceivedEventArgs)(ptr)
	request, err := args.GetRequest()
	if err != nil || request == nil {
		return 0
	}
	defer releaseNative(unsafe.Pointer(request))
	raw, err := nativeString(unsafe.Pointer(request), request.Vtbl.GetUri)
	if err != nil || !isReportURL(raw) {
		return 0
	}
	response, err := args.GetResponse()
	if err != nil || response == nil {
		return 0
	}
	defer releaseNative(unsafe.Pointer(response))
	var status int32
	hr, _, _ := response.Vtbl.GetStatusCode.Call(uintptr(unsafe.Pointer(response)), uintptr(unsafe.Pointer(&status)))
	if int32(hr) < 0 || status < 200 || status >= 300 {
		return 0
	}
	handler := combridge.New[contentCallback](invokeFunc(func(code uintptr, content unsafe.Pointer) uintptr {
		if int32(code) < 0 || content == nil || w.ctx.Err() != nil {
			return 0
		}
		body, err := io.ReadAll(io.LimitReader((*nativeIStream)(content), (1<<20)+1))
		if err == nil {
			acceptReport(w.session.ID, raw, body)
		}
		return 0
	}))
	defer handler.Close()
	response.Vtbl.GetContent.Call(uintptr(unsafe.Pointer(response)), handler.Ref())
	return 0
}

func nativeString(object unsafe.Pointer, getter nativeComProc) (string, error) {
	var value *uint16
	hr, _, _ := getter.Call(uintptr(object), uintptr(unsafe.Pointer(&value)))
	if int32(hr) < 0 {
		return "", syscall.Errno(hr)
	}
	if value == nil {
		return "", nil
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(value))
	return windows.UTF16PtrToString(value), nil
}

func releaseNative(object unsafe.Pointer) {
	if object != nil {
		(*nativeIUnknown)(object).Vtbl.CallRelease(object)
	}
}

// Minimal Win32 ABI declarations avoid importing the generated webview2 package:
// that package constructs invalid Go callback signatures during init in v1.0.22.
type nativeComProc uintptr

//go:uintptrescapes
func (p nativeComProc) Call(args ...uintptr) (uintptr, uintptr, error) {
	return syscall.SyscallN(uintptr(p), args...)
}

type nativeIUnknownVtbl struct{ QueryInterface, AddRef, Release nativeComProc }
type nativeIUnknown struct{ Vtbl *nativeIUnknownVtbl }

func (v *nativeIUnknownVtbl) CallRelease(p unsafe.Pointer) { v.Release.Call(uintptr(p)) }

type nativeRECT struct{ Left, Top, Right, Bottom int32 }
type nativeICoreWebView2Vtbl struct {
	nativeIUnknownVtbl
	GetSettings, GetSource, Navigate, NavigateToString                                                                   nativeComProc
	AddNavigationStarting, RemoveNavigationStarting                                                                      nativeComProc
	AddContentLoading, RemoveContentLoading                                                                              nativeComProc
	AddSourceChanged, RemoveSourceChanged                                                                                nativeComProc
	AddHistoryChanged, RemoveHistoryChanged                                                                              nativeComProc
	AddNavigationCompleted, RemoveNavigationCompleted                                                                    nativeComProc
	AddFrameNavigationStarting, RemoveFrameNavigationStarting                                                            nativeComProc
	AddFrameNavigationCompleted, RemoveFrameNavigationCompleted                                                          nativeComProc
	AddScriptDialogOpening, RemoveScriptDialogOpening                                                                    nativeComProc
	AddPermissionRequested, RemovePermissionRequested                                                                    nativeComProc
	AddProcessFailed, RemoveProcessFailed                                                                                nativeComProc
	AddScriptToExecuteOnDocumentCreated, RemoveScriptToExecuteOnDocumentCreated, ExecuteScript                           nativeComProc
	CapturePreview, Reload, PostWebMessageAsJSON, PostWebMessageAsString                                                 nativeComProc
	AddWebMessageReceived, RemoveWebMessageReceived, CallDevToolsProtocolMethod                                          nativeComProc
	GetBrowserProcessID, GetCanGoBack, GetCanGoForward, GoBack, GoForward                                                nativeComProc
	GetDevToolsProtocolEventReceiver, Stop                                                                               nativeComProc
	AddNewWindowRequested, RemoveNewWindowRequested                                                                      nativeComProc
	AddDocumentTitleChanged, RemoveDocumentTitleChanged, GetDocumentTitle                                                nativeComProc
	AddHostObjectToScript, RemoveHostObjectFromScript, OpenDevToolsWindow                                                nativeComProc
	AddContainsFullScreenElementChanged, RemoveContainsFullScreenElementChanged, GetContainsFullScreenElement            nativeComProc
	AddWebResourceRequested, RemoveWebResourceRequested, AddWebResourceRequestedFilter, RemoveWebResourceRequestedFilter nativeComProc
	AddWindowCloseRequested, RemoveWindowCloseRequested                                                                  nativeComProc
}
type nativeICoreWebView2 struct{ Vtbl *nativeICoreWebView2Vtbl }
type nativeControllerVtbl struct {
	nativeIUnknownVtbl
	GetIsVisible, PutIsVisible, GetBounds, PutBounds                                            nativeComProc
	GetZoomFactor, PutZoomFactor, AddZoomFactorChanged, RemoveZoomFactorChanged                 nativeComProc
	SetBoundsAndZoomFactor, MoveFocus, AddMoveFocusRequested, RemoveMoveFocusRequested          nativeComProc
	AddGotFocus, RemoveGotFocus, AddLostFocus, RemoveLostFocus                                  nativeComProc
	AddAcceleratorKeyPressed, RemoveAcceleratorKeyPressed                                       nativeComProc
	GetParentWindow, PutParentWindow, NotifyParentWindowPositionChanged, Close, GetCoreWebView2 nativeComProc
}
type nativeICoreWebView2Controller struct{ Vtbl *nativeControllerVtbl }

func (c *nativeICoreWebView2Controller) AddRef() { c.Vtbl.AddRef.Call(uintptr(unsafe.Pointer(c))) }
func (c *nativeICoreWebView2Controller) Close() error {
	hr, _, _ := c.Vtbl.Close.Call(uintptr(unsafe.Pointer(c)))
	return nativeError(hr)
}
func (c *nativeICoreWebView2Controller) PutBounds(r nativeRECT) error {
	hr, _, _ := c.Vtbl.PutBounds.Call(uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&r)))
	return nativeError(hr)
}
func (c *nativeICoreWebView2Controller) GetCoreWebView2() (*nativeICoreWebView2, error) {
	var v *nativeICoreWebView2
	hr, _, _ := c.Vtbl.GetCoreWebView2.Call(uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(&v)))
	return v, nativeError(hr)
}

type nativeEnvironmentVtbl struct {
	nativeIUnknownVtbl
	CreateCoreWebView2Controller nativeComProc
}
type nativeICoreWebView2Environment struct{ Vtbl *nativeEnvironmentVtbl }
type nativeSettingsVtbl struct {
	nativeIUnknownVtbl
	GetIsScriptEnabled, PutIsScriptEnabled, GetIsWebMessageEnabled, PutIsWebMessageEnabled     nativeComProc
	GetAreDefaultScriptDialogsEnabled, PutAreDefaultScriptDialogsEnabled                       nativeComProc
	GetIsStatusBarEnabled, PutIsStatusBarEnabled, GetAreDevToolsEnabled, PutAreDevToolsEnabled nativeComProc
	GetAreDefaultContextMenusEnabled, PutAreDefaultContextMenusEnabled                         nativeComProc
	GetAreHostObjectsAllowed, PutAreHostObjectsAllowed                                         nativeComProc
}
type nativeSettings struct{ Vtbl *nativeSettingsVtbl }

func (v *nativeICoreWebView2) GetSettings() (*nativeSettings, error) {
	var s *nativeSettings
	hr, _, _ := v.Vtbl.GetSettings.Call(uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(&s)))
	return s, nativeError(hr)
}
func (v *nativeICoreWebView2) Navigate(raw string) error {
	s, err := windows.UTF16PtrFromString(raw)
	if err != nil {
		return err
	}
	hr, _, _ := v.Vtbl.Navigate.Call(uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(s)))
	return nativeError(hr)
}

type nativeNavigationVtbl struct {
	nativeIUnknownVtbl
	GetUri, GetIsUserInitiated, GetIsRedirected, GetRequestHeaders, GetCancel, PutCancel nativeComProc
}
type nativeICoreWebView2NavigationStartingEventArgs struct{ Vtbl *nativeNavigationVtbl }
type nativePopupVtbl struct {
	nativeIUnknownVtbl
	GetUri, PutNewWindow, GetNewWindow, PutHandled nativeComProc
}
type nativeICoreWebView2NewWindowRequestedEventArgs struct{ Vtbl *nativePopupVtbl }
type nativeResponseArgsVtbl struct {
	nativeIUnknownVtbl
	GetRequest, GetResponse nativeComProc
}
type nativeICoreWebView2WebResourceResponseReceivedEventArgs struct{ Vtbl *nativeResponseArgsVtbl }
type nativeRequestVtbl struct {
	nativeIUnknownVtbl
	GetUri nativeComProc
}
type nativeRequest struct{ Vtbl *nativeRequestVtbl }
type nativeResponseVtbl struct {
	nativeIUnknownVtbl
	GetHeaders, GetStatusCode, GetReasonPhrase, GetContent nativeComProc
}
type nativeResponse struct{ Vtbl *nativeResponseVtbl }

func (a *nativeICoreWebView2WebResourceResponseReceivedEventArgs) GetRequest() (*nativeRequest, error) {
	var r *nativeRequest
	hr, _, _ := a.Vtbl.GetRequest.Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(&r)))
	return r, nativeError(hr)
}
func (a *nativeICoreWebView2WebResourceResponseReceivedEventArgs) GetResponse() (*nativeResponse, error) {
	var r *nativeResponse
	hr, _, _ := a.Vtbl.GetResponse.Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(&r)))
	return r, nativeError(hr)
}

type nativeStreamVtbl struct {
	nativeIUnknownVtbl
	Read nativeComProc
}
type nativeIStream struct{ Vtbl *nativeStreamVtbl }

func (s *nativeIStream) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	var n uint32
	hr, _, _ := s.Vtbl.Read.Call(uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))
	if int32(hr) < 0 {
		return int(n), syscall.Errno(hr)
	}
	if n == 0 || hr == 1 {
		return int(n), io.EOF
	}
	return int(n), nil
}
func nativeError(hr uintptr) error {
	if int32(hr) < 0 {
		return syscall.Errno(hr)
	}
	return nil
}
