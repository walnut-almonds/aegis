package lockclient_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lockservice/pb"
	"lockservice/sdk/lockclient"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- HTTP Client Tests ---

func TestHTTPClient_Acquire(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/lock/acquire" && r.Method == http.MethodPost:
			var req struct {
				Key    string `json:"key"`
				Token  string `json:"token"`
				TTLSec int    `json:"ttl_sec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			if req.Key != "test_key" {
				t.Errorf("expected key test_key, got %s", req.Key)
			}
			// Token must be provided by SDK (non-empty UUID)
			if req.Token == "" {
				t.Error("expected SDK-generated token, got empty string")
			}
			res := struct {
				Key       string    `json:"key"`
				Token     string    `json:"token"`
				ExpiredAt time.Time `json:"expired_at"`
			}{Key: req.Key, Token: req.Token, ExpiredAt: time.Now().Add(10 * time.Second)}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(res)
		case r.URL.Path == "/lock/release" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := lockclient.NewHTTPClient(ts.URL, 2*time.Second)
	defer client.Close()

	lock, err := client.Acquire(context.Background(), lockclient.AcquireOptions{
		Key:    "test_key",
		TTLSec: 10,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if lock.Key != "test_key" {
		t.Errorf("expected key test_key, got %s", lock.Key)
	}
	if lock.Token() == "" {
		t.Error("expected non-empty token from SDK")
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Errorf("release failed: %v", err)
	}
}

func TestHTTPClient_WithLock(t *testing.T) {
	var acquireCalled, releaseCalled bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/lock/acquire":
			acquireCalled = true
			var req struct {
				Key   string `json:"key"`
				Token string `json:"token"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			res := struct {
				Key       string    `json:"key"`
				Token     string    `json:"token"`
				ExpiredAt time.Time `json:"expired_at"`
			}{Key: req.Key, Token: req.Token, ExpiredAt: time.Now().Add(30 * time.Second)}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(res)
		case r.URL.Path == "/lock/release":
			releaseCalled = true
			w.Write([]byte(`{}`))
		case r.URL.Path == "/lock/extend":
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client := lockclient.NewHTTPClient(ts.URL, 2*time.Second)
	defer client.Close()

	fnCalled := false
	err := client.WithLock(context.Background(), "resource", 5*time.Second, func(ctx context.Context) error {
		fnCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock failed: %v", err)
	}
	if !acquireCalled {
		t.Error("acquire was not called")
	}
	if !fnCalled {
		t.Error("fn was not called")
	}
	if !releaseCalled {
		t.Error("release was not called")
	}
}

func TestHTTPClient_Retry(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/lock/acquire":
			attempts++
			if attempts < 3 {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			var req struct {
				Key   string `json:"key"`
				Token string `json:"token"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			res := struct {
				Key       string    `json:"key"`
				Token     string    `json:"token"`
				ExpiredAt time.Time `json:"expired_at"`
			}{Key: req.Key, Token: req.Token, ExpiredAt: time.Now().Add(10 * time.Second)}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(res)
		case r.URL.Path == "/lock/release":
			w.Write([]byte(`{}`))
		}
	}))
	defer ts.Close()

	client := lockclient.NewHTTPClient(ts.URL, 2*time.Second)
	defer client.Close()

	lock, err := client.Acquire(context.Background(), lockclient.AcquireOptions{
		Key:           "retry_key",
		TTLSec:        10,
		RetryCount:    3,
		RetryInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	lock.Release(context.Background())
}

// --- gRPC Client Tests ---

type mockGRPCServer struct {
	pb.UnimplementedLockServiceServer
}

func (m *mockGRPCServer) Acquire(ctx context.Context, req *pb.LockRequest) (*pb.LockResponse, error) {
	if req.Key == "fail" {
		return nil, status.Error(codes.AlreadyExists, "lock already exists")
	}
	return &pb.LockResponse{
		Key:       req.Key,
		Token:     req.Token,
		ExpiredAt: time.Now().Add(10 * time.Second).Unix(),
	}, nil
}

func (m *mockGRPCServer) Release(ctx context.Context, req *pb.LockRequest) (*pb.ActionResponse, error) {
	return &pb.ActionResponse{Status: "released"}, nil
}

func (m *mockGRPCServer) Extend(ctx context.Context, req *pb.LockRequest) (*pb.ActionResponse, error) {
	return &pb.ActionResponse{Status: "extended"}, nil
}

func startMockGRPC(t *testing.T) (lockclient.Client, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	pb.RegisterLockServiceServer(s, &mockGRPCServer{})
	go func() {
		if err := s.Serve(lis); err != nil {
			// server stopped normally
		}
	}()
	client, err := lockclient.NewGRPCClient(context.Background(), lis.Addr().String())
	if err != nil {
		s.Stop()
		lis.Close()
		t.Fatalf("failed to create client: %v", err)
	}
	return client, func() {
		client.Close()
		s.Stop()
		lis.Close()
	}
}

func TestGRPCClient_Acquire(t *testing.T) {
	client, cleanup := startMockGRPC(t)
	defer cleanup()

	lock, err := client.Acquire(context.Background(), lockclient.AcquireOptions{
		Key:    "test_key",
		TTLSec: 10,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if lock.Key != "test_key" {
		t.Errorf("expected key test_key, got %s", lock.Key)
	}
	if lock.Token() == "" {
		t.Error("expected non-empty SDK-generated token")
	}

	if err := lock.Release(context.Background()); err != nil {
		t.Errorf("release failed: %v", err)
	}
}

func TestGRPCClient_AcquireFailure(t *testing.T) {
	client, cleanup := startMockGRPC(t)
	defer cleanup()

	_, err := client.Acquire(context.Background(), lockclient.AcquireOptions{
		Key:    "fail",
		TTLSec: 10,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPCClient_WithLock(t *testing.T) {
	client, cleanup := startMockGRPC(t)
	defer cleanup()

	called := false
	err := client.WithLock(context.Background(), "resource", 5*time.Second, func(ctx context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock failed: %v", err)
	}
	if !called {
		t.Error("fn was not called")
	}
}

func TestGRPCClient_AutoRenew(t *testing.T) {
	client, cleanup := startMockGRPC(t)
	defer cleanup()

	lock, err := client.Acquire(context.Background(), lockclient.AcquireOptions{
		Key:            "renew_key",
		TTLSec:         2,
		AutoRenew:      true,
		RenewThreshold: 0.5,
	})
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	// Let auto-renew fire at least once (TTL=2s, renew at 1s)
	time.Sleep(1200 * time.Millisecond)
	if err := lock.Release(context.Background()); err != nil {
		t.Errorf("release failed: %v", err)
	}
}
