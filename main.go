package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"lockservice/pb"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- Domain Models ---

// LockRequest 代表所有 Lock, Extend, Unlock 的通用輸入請求
type LockRequest struct {
	Key    string `json:"key"`     // 鎖的唯一鍵值 (e.g., book:1)
	Token  string `json:"token"`   // 擁有者的唯一識別碼
	TTLSec int    `json:"ttl_sec"` // 鎖定存活時間(秒)
}

// Lock 代表目前的鎖資源資訊
type Lock struct {
	Key       string    `json:"key"`
	Token     string    `json:"token"`
	ExpiredAt time.Time `json:"expired_at"`
}

// --- Storage Interface & In-Memory Implementation ---

// LockStorage 定義分散式鎖後端的介面 (未來可抽換成 Redis/DynamoDB)
type LockStorage interface {
	Acquire(req LockRequest) (*Lock, error)
	Release(req LockRequest) error
	Extend(req LockRequest) error
}

var (
	ErrLockAlreadyExists = errors.New("lock already acquired by others")
	ErrLockNotFound      = errors.New("lock not found")
	ErrTokenMismatch     = errors.New("unexpected token")
	ErrLockExpired       = errors.New("lock expired")
)

// --- HTTP Handlers ---

func handleAcquire(store LockStorage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req LockRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		lock, err := store.Acquire(req)
		if err != nil {
			status := http.StatusConflict
			http.Error(w, err.Error(), status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(lock)
	}
}

func handleRelease(store LockStorage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req LockRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		if err := store.Release(req); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "released"}`))
	}
}

func handleExtend(store LockStorage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req LockRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		if err := store.Extend(req); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "extended"}`))
	}
}

// --- gRPC Server Implementation ---

type grpcLockServer struct {
	pb.UnimplementedLockServiceServer
	store LockStorage
}

func (s *grpcLockServer) Acquire(ctx context.Context, req *pb.LockRequest) (*pb.LockResponse, error) {
	domainReq := LockRequest{
		Key:    req.Key,
		Token:  req.Token,
		TTLSec: int(req.TtlSec),
	}
	lock, err := s.store.Acquire(domainReq)
	if err != nil {
		if errors.Is(err, ErrLockAlreadyExists) {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.LockResponse{
		Key:       lock.Key,
		Token:     lock.Token,
		ExpiredAt: lock.ExpiredAt.Unix(),
	}, nil
}

func (s *grpcLockServer) Release(ctx context.Context, req *pb.LockRequest) (*pb.ActionResponse, error) {
	domainReq := LockRequest{
		Key:   req.Key,
		Token: req.Token,
	}
	err := s.store.Release(domainReq)
	if err != nil {
		if errors.Is(err, ErrLockNotFound) || errors.Is(err, ErrTokenMismatch) || errors.Is(err, ErrLockExpired) {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.ActionResponse{Status: "released"}, nil
}

func (s *grpcLockServer) Extend(ctx context.Context, req *pb.LockRequest) (*pb.ActionResponse, error) {
	domainReq := LockRequest{
		Key:    req.Key,
		Token:  req.Token,
		TTLSec: int(req.TtlSec),
	}
	err := s.store.Extend(domainReq)
	if err != nil {
		if errors.Is(err, ErrLockNotFound) || errors.Is(err, ErrTokenMismatch) || errors.Is(err, ErrLockExpired) {
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.ActionResponse{Status: "extended"}, nil
}

func main() {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:16379", // Redis server setting
	})
	store := NewRedisLockStore(rdb)

	// 啟動 gRPC 伺服器
	go func() {
		lis, err := net.Listen("tcp", ":50051")
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		s := grpc.NewServer()
		pb.RegisterLockServiceServer(s, &grpcLockServer{store: store})
		log.Println("gRPC Server listening on :50051...")
		if err := s.Serve(lis); err != nil {
			log.Fatalf("failed to serve grpc: %v", err)
		}
	}()

	// 註冊 RESTful API Routes
	http.HandleFunc("/lock/acquire", handleAcquire(store))
	http.HandleFunc("/lock/release", handleRelease(store))
	http.HandleFunc("/lock/extend", handleExtend(store))

	log.Println("Lock Service listening on :8080...")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}
