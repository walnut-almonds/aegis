package lockclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type httpTransport struct {
	client  *http.Client
	baseURL string
}

// NewHTTPClient creates a Client backed by HTTP/JSON transport.
func NewHTTPClient(baseURL string, timeout time.Duration) Client {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	t := &httpTransport{
		client:  &http.Client{Timeout: timeout},
		baseURL: baseURL,
	}
	return newClient(t)
}

type lockPayload struct {
	Key    string `json:"key"`
	Token  string `json:"token"`
	TTLSec int    `json:"ttl_sec,omitempty"`
}

type lockRespPayload struct {
	Key       string    `json:"key"`
	Token     string    `json:"token"`
	ExpiredAt time.Time `json:"expired_at"`
}

func (t *httpTransport) acquire(
	ctx context.Context,
	key, token string,
	ttlSec int,
) (*LockResponse, error) {
	body, err := t.doReq(
		ctx,
		http.MethodPost,
		"/lock/acquire",
		lockPayload{Key: key, Token: token, TTLSec: ttlSec},
	)
	if err != nil {
		return nil, err
	}
	var res lockRespPayload
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &LockResponse{Key: res.Key, Token: res.Token, ExpiredAt: res.ExpiredAt}, nil
}

func (t *httpTransport) release(ctx context.Context, key, token string) error {
	_, err := t.doReq(ctx, http.MethodPost, "/lock/release", lockPayload{Key: key, Token: token})
	return err
}

func (t *httpTransport) extend(ctx context.Context, key, token string, ttlSec int) error {
	_, err := t.doReq(
		ctx,
		http.MethodPost,
		"/lock/extend",
		lockPayload{Key: key, Token: token, TTLSec: ttlSec},
	)
	return err
}

func (t *httpTransport) close() error { return nil }

func (t *httpTransport) doReq(
	ctx context.Context,
	method, path string,
	payload any,
) ([]byte, error) {
	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(
		ctx,
		method,
		t.baseURL+path,
		bytes.NewReader(reqBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer func() {
		_ = res.Body.Close()
	}()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("http error: %s - %s", res.Status, string(body))
	}
	return body, nil
}
