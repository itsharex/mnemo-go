package netx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestClientDoCancelsBlockedTransport(t *testing.T) {
	started := make(chan struct{}, 1)
	previous := TestTransportHook
	TestTransportHook = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	t.Cleanup(func() { TestTransportHook = previous })

	client := NewClient(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Do(ctx, http.MethodGet, "http://127.0.0.1:1/blocked", nil, nil)
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("transport did not receive request")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Do error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Do did not return after cancellation")
	}
}

func TestNormalizeSystemProxy(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "prefer HTTPS entry", raw: "http=127.0.0.1:7890;https=127.0.0.1:7891", want: "http://127.0.0.1:7891"},
		{name: "bare endpoint", raw: "127.0.0.1:7890", want: "http://127.0.0.1:7890"},
		{name: "SOCKS entry", raw: "socks=127.0.0.1:1080", want: "socks5://127.0.0.1:1080"},
		{name: "ignore unsupported entry", raw: "ftp=127.0.0.1:21", want: ""},
		{name: "reject hostless URL", raw: "http://", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeSystemProxy(test.raw); got != test.want {
				t.Fatalf("normalizeSystemProxy(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func TestUploadRequestsShareLimitAndPreserveReplay(t *testing.T) {
	SetGlobalUploadRate(2000)
	defer SetGlobalUploadRate(0)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.ContentLength != 200 || req.GetBody == nil {
			t.Error("upload length/replay lost")
		}
		b, err := io.ReadAll(req.Body)
		if err != nil || len(b) != 200 {
			t.Errorf("body: %d %v", len(b), err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	})}
	start := time.Now()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, "https://upload.example/part", bytes.NewReader(make([]byte, 200)))
			resp, err := DoUpload(client, req)
			if err != nil {
				t.Error(err)
			} else {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("parallel uploads bypass aggregate limit")
	}
}

func TestUploadWaitCancelsAndRespondsToRateChanges(t *testing.T) {
	SetGlobalUploadRate(1)
	defer SetGlobalUploadRate(0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- WaitGlobalUploadContext(ctx, 100) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upload wait ignored cancellation")
	}
	go func() { done <- WaitGlobalUploadContext(context.Background(), 100) }()
	SetGlobalUploadRate(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rate change did not release upload")
	}
}

func TestUploadRedirectReplaysAndThrottlesBody(t *testing.T) {
	SetGlobalUploadRate(1000)
	defer SetGlobalUploadRate(0)
	var lengths []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		lengths = append(lengths, len(body))
		if r.ContentLength != 200 {
			t.Errorf("content length=%d", r.ContentLength)
		}
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "/end")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL+"/start", bytes.NewReader(make([]byte, 200)))
	start := time.Now()
	resp, err := DoUpload(srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(lengths) != 2 || lengths[0] != 200 || lengths[1] != 200 {
		t.Fatalf("replay lengths=%v", lengths)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("replayed bytes bypassed throttle")
	}
}

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
