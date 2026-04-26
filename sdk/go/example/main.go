// Package main demonstrates common usage patterns of the lockclient SDK.
//
// Covered scenarios:
//  1. Basic acquire / release (HTTP transport)
//  2. Acquire with retry
//  3. Manual TTL extension
//  4. Auto-renew (long-running critical section)
//  5. WithLock helper (acquire + run + release in one call)
//  6. Competitive locking (only one goroutine wins)
//  7. Context cancellation / timeout
//  8. gRPC transport (same API, different constructor)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/walnut-almonds/aegis/sdk/go/lockclient"
)

const (
	httpAddr = "http://localhost:8080"
	grpcAddr = "localhost:9090"
)

func main() {
	// ── HTTP client ──────────────────────────────────────────────────────────
	httpClient := lockclient.NewHTTPClient(httpAddr, 5*time.Second)
	defer func() {
		if err := httpClient.Close(); err != nil {
			log.Printf("[HTTP] close failed: %v", err)
		}
	}()

	scenario1_BasicAcquireRelease(httpClient)
	scenario2_AcquireWithRetry(httpClient)
	scenario3_ManualExtend(httpClient)
	scenario4_AutoRenew(httpClient)
	scenario5_WithLock(httpClient)
	scenario6_CompetitiveLocking(httpClient)
	scenario7_ContextTimeout(httpClient)

	// ── gRPC client (same scenarios work identically) ────────────────────────
	ctx := context.Background()
	grpcClient, err := lockclient.NewGRPCClient(ctx, grpcAddr)
	if err != nil {
		log.Printf("[gRPC] failed to create client: %v", err)
		return
	}
	defer func() { _ = grpcClient.Close() }()

	scenario8_GRPCBasic(grpcClient)
}

// ── Scenario 1 ────────────────────────────────────────────────────────────────
// 最基本的用法：取得鎖、執行工作、釋放鎖。
func scenario1_BasicAcquireRelease(c lockclient.Client) {
	fmt.Println("\n=== Scenario 1: Basic Acquire / Release ===")

	ctx := context.Background()

	lock, err := c.Acquire(ctx, lockclient.AcquireOptions{
		Key:    "resource:order-123",
		TTLSec: 10,
	})
	if err != nil {
		log.Printf("acquire failed: %v", err)
		return
	}

	fmt.Printf("  acquired  key=%s  token=%s  expires=%s\n",
		lock.Key, lock.Token(), lock.ExpiredAt.Format(time.RFC3339))

	// --- 在這裡執行需要互斥保護的工作 ---
	doWork("update order-123")

	if err := lock.Release(ctx); err != nil {
		log.Printf("release failed: %v", err)
		return
	}
	fmt.Println("  released")
}

// ── Scenario 2 ────────────────────────────────────────────────────────────────
// 若鎖已被佔用，自動重試最多 5 次，每次間隔 300 ms。
func scenario2_AcquireWithRetry(c lockclient.Client) {
	fmt.Println("\n=== Scenario 2: Acquire with Retry ===")

	ctx := context.Background()

	lock, err := c.Acquire(ctx, lockclient.AcquireOptions{
		Key:           "resource:inventory",
		TTLSec:        10,
		RetryCount:    5,
		RetryInterval: 300 * time.Millisecond,
	})
	if err != nil {
		log.Printf("acquire with retry failed: %v", err)
		return
	}
	defer func() { _ = lock.Release(ctx) }() // 確保一定會釋放

	fmt.Printf("  acquired after retry  key=%s\n", lock.Key)
	doWork("check inventory")
}

// ── Scenario 3 ────────────────────────────────────────────────────────────────
// 手動延長鎖的 TTL（適合已知工作還需更多時間的情況）。
func scenario3_ManualExtend(c lockclient.Client) {
	fmt.Println("\n=== Scenario 3: Manual TTL Extension ===")

	ctx := context.Background()

	lock, err := c.Acquire(ctx, lockclient.AcquireOptions{
		Key:    "resource:report-gen",
		TTLSec: 5,
	})
	if err != nil {
		log.Printf("acquire failed: %v", err)
		return
	}
	defer func() { _ = lock.Release(ctx) }()

	fmt.Printf("  acquired  expires=%s\n", lock.ExpiredAt.Format(time.RFC3339))

	// 模擬工作進行到一半，發現還需要更多時間
	time.Sleep(3 * time.Second)

	if err := lock.Extend(ctx, 10); err != nil {
		log.Printf("extend failed: %v", err)
		return
	}
	fmt.Printf("  extended  new expiry≈%s\n",
		time.Now().Add(10*time.Second).Format(time.RFC3339))

	doWork("generate report (continued)")
}

// ── Scenario 4 ────────────────────────────────────────────────────────────────
// AutoRenew：SDK 在背景自動續約，無需手動呼叫 Extend。
// 適合執行時間不確定的長時間任務。
func scenario4_AutoRenew(c lockclient.Client) {
	fmt.Println("\n=== Scenario 4: Auto-Renew ===")

	ctx := context.Background()

	lock, err := c.Acquire(ctx, lockclient.AcquireOptions{
		Key:            "resource:data-migration",
		TTLSec:         6,
		AutoRenew:      true,
		RenewThreshold: 0.4, // 剩餘 40% TTL 時開始續約
	})
	if err != nil {
		log.Printf("acquire failed: %v", err)
		return
	}

	fmt.Printf("  acquired with auto-renew  key=%s\n", lock.Key)

	// 模擬需要超過一個 TTL 週期的長時間工作
	doLongWork("data migration", 8*time.Second)

	// Release 同時會停止背景續約 goroutine
	if err := lock.Release(ctx); err != nil {
		log.Printf("release failed: %v", err)
	}
	fmt.Println("  released (auto-renew stopped)")
}

// ── Scenario 5 ────────────────────────────────────────────────────────────────
// WithLock：便利方法，自動 acquire + auto-renew + release。
// 最簡潔的用法，推薦給大多數使用場景。
func scenario5_WithLock(c lockclient.Client) {
	fmt.Println("\n=== Scenario 5: WithLock Helper ===")

	ctx := context.Background()

	err := c.WithLock(ctx, "resource:cache-rebuild", 15*time.Second,
		func(ctx context.Context) error {
			fmt.Println("  inside WithLock — rebuilding cache...")
			doWork("rebuild cache")
			return nil // 返回 error 會向上傳遞，但鎖仍會被釋放
		},
	)
	if err != nil {
		log.Printf("WithLock failed: %v", err)
		return
	}
	fmt.Println("  WithLock completed")
}

// ── Scenario 6 ────────────────────────────────────────────────────────────────
// 競爭鎖定：多個 goroutine 同時嘗試取得同一把鎖，
// 只有一個會成功（或透過 retry 等待前者釋放後取得）。
func scenario6_CompetitiveLocking(c lockclient.Client) {
	fmt.Println("\n=== Scenario 6: Competitive Locking (3 goroutines) ===")

	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			lock, err := c.Acquire(ctx, lockclient.AcquireOptions{
				Key:           "resource:shared-counter",
				TTLSec:        5,
				RetryCount:    10,
				RetryInterval: 200 * time.Millisecond,
			})
			if err != nil {
				fmt.Printf("  goroutine %d: could not acquire: %v\n", id, err)
				return
			}
			fmt.Printf("  goroutine %d: acquired lock\n", id)
			time.Sleep(500 * time.Millisecond) // 模擬互斥工作
			if err := lock.Release(ctx); err != nil {
				fmt.Printf("  goroutine %d: release error: %v\n", id, err)
				return
			}
			fmt.Printf("  goroutine %d: released lock\n", id)
		}(i)
	}
	wg.Wait()
	fmt.Println("  all goroutines done")
}

// ── Scenario 7 ────────────────────────────────────────────────────────────────
// Context timeout / cancellation：
// 若等待鎖的過程超過 deadline，Acquire 會立即返回錯誤。
func scenario7_ContextTimeout(c lockclient.Client) {
	fmt.Println("\n=== Scenario 7: Context Timeout ===")

	// 給一個只有 500 ms 的超時
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	lock, err := c.Acquire(ctx, lockclient.AcquireOptions{
		Key:           "resource:tight-deadline",
		TTLSec:        10,
		RetryCount:    20,
		RetryInterval: 100 * time.Millisecond,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			fmt.Printf("  expected: context expired before lock acquired (%v)\n", err)
		} else {
			fmt.Printf("  acquire error: %v\n", err)
		}
		return
	}
	// 如果鎖立刻可用就正常使用
	defer func() {
		if err := lock.Release(context.Background()); err != nil {
			log.Printf("release failed: %v", err)
		}
	}()
	fmt.Printf("  acquired key=%s\n", lock.Key)
}

// ── Scenario 8 ────────────────────────────────────────────────────────────────
// gRPC transport 使用方式與 HTTP 完全相同，只差在 Client 建立方式。
func scenario8_GRPCBasic(c lockclient.Client) {
	fmt.Println("\n=== Scenario 8: gRPC Transport — Basic Acquire / Release ===")

	ctx := context.Background()

	err := c.WithLock(ctx, "resource:grpc-demo", 10*time.Second,
		func(ctx context.Context) error {
			doWork("critical section over gRPC")
			return nil
		},
	)
	if err != nil {
		log.Printf("gRPC WithLock failed: %v", err)
		return
	}
	fmt.Println("  gRPC WithLock completed")
}

// ── helpers ───────────────────────────────────────────────────────────────────

func doWork(name string) {
	fmt.Printf("  [work] %s ...\n", name)
	time.Sleep(200 * time.Millisecond)
	fmt.Printf("  [work] %s done\n", name)
}

func doLongWork(name string, d time.Duration) {
	fmt.Printf("  [long work] %s started (duration=%s)\n", name, d)
	time.Sleep(d)
	fmt.Printf("  [long work] %s done\n", name)
}
