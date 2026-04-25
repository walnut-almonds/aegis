package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"lockservice/pb"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
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

// loadConfig 初始化 Viper，讀取 config.yaml 並設定預設值
func loadConfig() {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".") // 優先讀取執行目錄下的 config.yaml

	// 預設值（當 config.yaml 不存在時仍可運作）
	viper.SetDefault("server.http_port", ":8080")
	viper.SetDefault("server.grpc_port", ":50051")
	viper.SetDefault("backend", "redis")
	viper.SetDefault("redis.addr", "localhost:16379")
	viper.SetDefault("dynamodb.table_name", "locks")
	viper.SetDefault("dynamodb.region", "us-east-1")
	viper.SetDefault("dynamodb.endpoint", "")
	viper.SetDefault("sql.dsn", "")
	viper.SetDefault("sql.dialect", "postgres")

	// 允許透過環境變數覆蓋（e.g., REDIS_ADDR、SERVER_HTTP_PORT）
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			log.Fatalf("failed to read config: %v", err)
		}
		log.Println("config.yaml not found, using defaults / environment variables")
	} else {
		log.Printf("config loaded from: %s", viper.ConfigFileUsed())
	}
}

// newStore 根據設定的 backend 建立對應的 LockStorage
func newStore() LockStorage {
	switch viper.GetString("backend") {
	case "redis":
		rdb := redis.NewClient(&redis.Options{
			Addr: viper.GetString("redis.addr"),
		})
		log.Printf("using Redis backend at %s", viper.GetString("redis.addr"))
		return NewRedisLockStore(rdb)

	case "dynamodb":
		ctx := context.Background()
		cfg, err := awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(viper.GetString("dynamodb.region")),
		)
		if err != nil {
			log.Fatalf("failed to load AWS config: %v", err)
		}
		client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
			if ep := viper.GetString("dynamodb.endpoint"); ep != "" {
				o.BaseEndpoint = aws.String(ep)
			}
		})
		tableName := viper.GetString("dynamodb.table_name")
		log.Printf("using DynamoDB backend, table=%s region=%s", tableName, viper.GetString("dynamodb.region"))
		return NewDynamoDBLockStore(client, tableName)

	case "postgres":
		db, err := sql.Open("postgres", viper.GetString("sql.dsn"))
		if err != nil {
			log.Fatalf("failed to open postgres: %v", err)
		}
		store, err := NewSQLLockStore(db, DialectPostgres)
		if err != nil {
			log.Fatalf("failed to init postgres store: %v", err)
		}
		log.Println("using PostgreSQL backend")
		return store

	case "mysql":
		db, err := sql.Open("mysql", viper.GetString("sql.dsn"))
		if err != nil {
			log.Fatalf("failed to open mysql: %v", err)
		}
		store, err := NewSQLLockStore(db, DialectMySQL)
		if err != nil {
			log.Fatalf("failed to init mysql store: %v", err)
		}
		log.Println("using MySQL backend")
		return store

	default:
		log.Fatalf("unknown backend: %q (valid: redis, dynamodb, postgres, mysql)", viper.GetString("backend"))
		return nil
	}
}

func main() {
	loadConfig()

	store := newStore()

	httpPort := viper.GetString("server.http_port")
	grpcPort := viper.GetString("server.grpc_port")

	// 啟動 gRPC 伺服器
	go func() {
		lis, err := net.Listen("tcp", grpcPort)
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		s := grpc.NewServer()
		pb.RegisterLockServiceServer(s, &grpcLockServer{store: store})
		log.Printf("gRPC Server listening on %s...", grpcPort)
		if err := s.Serve(lis); err != nil {
			log.Fatalf("failed to serve grpc: %v", err)
		}
	}()

	// 註冊 RESTful API Routes
	http.HandleFunc("/lock/acquire", handleAcquire(store))
	http.HandleFunc("/lock/release", handleRelease(store))
	http.HandleFunc("/lock/extend", handleExtend(store))

	log.Printf("Lock Service listening on %s...", httpPort)
	if err := http.ListenAndServe(httpPort, nil); err != nil {
		log.Fatal(err)
	}
}
