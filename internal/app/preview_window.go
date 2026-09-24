package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"mnemo-go/internal/model"
)

// PreviewWindowSeed contains display metadata only, never account credentials.
type PreviewWindowAccount struct {
	UserID  string `json:"user_id"`
	DriveID string `json:"drive_id"`
}

type PreviewWindowSeed struct {
	Account      PreviewWindowAccount `json:"account"`
	File         model.File           `json:"file"`
	Files        []model.File         `json:"files"`
	Capabilities map[string]any       `json:"capabilities"`
	Kind         string               `json:"kind"`
	Preferences  map[string]any       `json:"preferences"`
}

type previewChildConfig struct {
	Endpoint string            `json:"endpoint"`
	Token    string            `json:"token"`
	Seed     PreviewWindowSeed `json:"seed"`
}

// PreviewHost runs in a separate Wails process. All storage and provider work
// remains in the main process, avoiding competing stores and transfer queues.
type PreviewHost struct {
	config  previewChildConfig
	ctx     context.Context
	closing atomic.Bool
}

func (h *PreviewHost) Startup(ctx context.Context) {
	h.ctx = ctx
	go func() {
		for !h.closing.Load() {
			result, err := h.Invoke("_WindowCommand", nil)
			if err != nil {
				return
			}
			if string(result) == `"show"` {
				runtime.WindowUnminimise(ctx)
				runtime.WindowShow(ctx)
				runtime.WindowSetAlwaysOnTop(ctx, true)
				runtime.WindowSetAlwaysOnTop(ctx, false)
			}
		}
	}()
}
func (h *PreviewHost) BeforeClose(ctx context.Context) bool {
	if h.closing.Load() {
		return false
	}
	runtime.EventsEmit(ctx, "preview:close-request")
	return true
}
func (h *PreviewHost) Close() { h.closing.Store(true); runtime.Quit(h.ctx) }

func ReadPreviewHost(r io.Reader) (*PreviewHost, error) {
	var config previewChildConfig
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&config); err != nil {
		return nil, err
	}
	return &PreviewHost{config: config}, nil
}

func (h *PreviewHost) Config() PreviewWindowSeed { return h.config.Seed }

type previewCall struct {
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}
type previewReply struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

func (h *PreviewHost) Invoke(method string, args []json.RawMessage) (json.RawMessage, error) {
	body, err := json.Marshal(previewCall{method, args})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, h.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.config.Token)
	client := &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{Proxy: nil}}
	if method == "SaveCloudTextFile" {
		client.Timeout = 31 * time.Minute
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("主窗口连接已断开，请重新打开预览")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("预览请求失败：HTTP %d", resp.StatusCode)
	}
	var reply previewReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&reply); err != nil {
		return nil, err
	}
	if reply.Error != "" {
		return nil, errors.New(reply.Error)
	}
	return reply.Result, nil
}

var previewMethods = map[string]bool{
	"PreviewURL": true, "CachedPreviewImageURL": true, "PinFileSnapshot": true, "PlayVideo": true,
	"PlayVideoQuality": true, "GetPlayCursor": true, "SavePlayCursor": true,
	"GetSettings": true, "DownloadFile": true, "SaveCloudTextFile": true,
}

type previewProcess struct {
	cancel   context.CancelFunc
	commands chan string
}

func (a *App) ShowPreviewWindows() {
	a.previewWindows.Range(func(_, value any) bool {
		select {
		case value.(*previewProcess).commands <- "show":
		default:
		}
		return true
	})
}

func (a *App) previewInvoke(seed PreviewWindowSeed, call previewCall) (result previewReply) {
	defer func() {
		if recover() != nil {
			result = previewReply{Error: "预览请求参数无效"}
		}
	}()
	if !previewMethods[call.Method] {
		return previewReply{Error: "预览窗口不支持此操作"}
	}
	if call.Method != "GetSettings" {
		var uid, did string
		if len(call.Args) < 2 || json.Unmarshal(call.Args[0], &uid) != nil || json.Unmarshal(call.Args[1], &did) != nil || uid != seed.Account.UserID || did != seed.Account.DriveID {
			return previewReply{Error: "预览账号不匹配"}
		}
	}
	method := reflect.ValueOf(a).MethodByName(call.Method)
	if !method.IsValid() || method.Type().NumIn() != len(call.Args) {
		return previewReply{Error: "预览请求参数无效"}
	}
	args := make([]reflect.Value, len(call.Args))
	for i, raw := range call.Args {
		v := reflect.New(method.Type().In(i))
		if err := json.Unmarshal(raw, v.Interface()); err != nil {
			return previewReply{Error: err.Error()}
		}
		args[i] = v.Elem()
	}
	out := method.Call(args)
	var value any
	for _, v := range out {
		if v.Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			if !v.IsNil() {
				return previewReply{Error: v.Interface().(error).Error()}
			}
		} else {
			value = v.Interface()
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return previewReply{Error: err.Error()}
	}
	if call.Method == "SaveCloudTextFile" {
		if ctx, ok := a.wailsContext(); ok {
			runtime.EventsEmit(ctx, "preview:saved", seed.Account.UserID, seed.Account.DriveID)
		}
	}
	return previewReply{Result: data}
}

func (a *App) OpenPreviewWindow(seed PreviewWindowSeed) error {
	if a.previewStopping.Load() {
		return errors.New("应用正在退出")
	}
	if seed.Account.UserID == "" || seed.File.FileID == "" {
		return errors.New("预览文件或账号无效")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		listener.Close()
		return err
	}
	token := hex.EncodeToString(key)
	process := &previewProcess{commands: make(chan string, 1)}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var call previewCall
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&call) != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if call.Method == "_WindowCommand" {
			command := ""
			select {
			case command = <-process.commands:
			case <-time.After(20 * time.Second):
			case <-r.Context().Done():
				return
			}
			data, _ := json.Marshal(command)
			_ = json.NewEncoder(w).Encode(previewReply{Result: data})
			return
		}
		_ = json.NewEncoder(w).Encode(a.previewInvoke(seed, call))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, executable, "--mnemo-preview")
	config, err := json.Marshal(previewChildConfig{Endpoint: "http://" + listener.Addr().String(), Token: token, Seed: seed})
	if err != nil {
		cancel()
		listener.Close()
		return err
	}
	cmd.Stdin = bytes.NewReader(config)
	if err := cmd.Start(); err != nil {
		cancel()
		listener.Close()
		return err
	}
	process.cancel = cancel
	a.previewWindows.Store(cmd.Process.Pid, process)
	if a.previewStopping.Load() {
		cancel()
	}
	go func() { _ = server.Serve(listener) }()
	go func() { _ = cmd.Wait(); cancel(); _ = server.Close(); a.previewWindows.Delete(cmd.Process.Pid) }()
	return nil
}
