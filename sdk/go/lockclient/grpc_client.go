package lockclient

import (
	"context"
	"fmt"
	"time"

	"github.com/walnut-almonds/aegis/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type grpcTransport struct {
	conn   *grpc.ClientConn
	client pb.LockServiceClient
}

// NewGRPCClient creates a Client backed by gRPC transport.
func NewGRPCClient(ctx context.Context, target string) (Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial target %s: %w", target, err)
	}
	t := &grpcTransport{
		conn:   conn,
		client: pb.NewLockServiceClient(conn),
	}
	return newClient(t), nil
}

func (t *grpcTransport) acquire(
	ctx context.Context,
	key, token string,
	ttlSec int,
) (*LockResponse, error) {
	res, err := t.client.Acquire(ctx, &pb.LockRequest{
		Key:    key,
		Token:  token,
		TtlSec: int32(ttlSec),
	})
	if err != nil {
		return nil, err
	}
	return &LockResponse{
		Key:       res.Key,
		Token:     res.Token,
		ExpiredAt: time.Unix(res.ExpiredAt, 0),
	}, nil
}

func (t *grpcTransport) release(ctx context.Context, key, token string) error {
	_, err := t.client.Release(ctx, &pb.LockRequest{Key: key, Token: token})
	return err
}

func (t *grpcTransport) extend(ctx context.Context, key, token string, ttlSec int) error {
	_, err := t.client.Extend(ctx, &pb.LockRequest{Key: key, Token: token, TtlSec: int32(ttlSec)})
	return err
}

func (t *grpcTransport) close() error {
	return t.conn.Close()
}
