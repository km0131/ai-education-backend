package utils

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type ipRateWindow struct {
	count      int
	windowEnds time.Time
}

// PerIPRateLimiter is a minimal fixed-window per-client-IP rate limiter
// (公開プレビューのリバースプロキシ向け、NextPlan.md フェーズ7「悪用対策」)。
// 外部ライブラリ(golang.org/x/time/rate等)を追加しないための自前実装 -
// 固定ウィンドウ方式(windowごとにカウンタをリセットするだけ)はトークン
// バケット等よりウィンドウ境界付近でのバーストを許してしまう荒さがあるが、
// 教育用サンドボックスのプレビュー機能程度の規模では十分な精度。
// maxRequestsは1クライアントIPあたりwindow時間内に許可するリクエスト数。
func PerIPRateLimiter(maxRequests int, window time.Duration) gin.HandlerFunc {
	var mu sync.Mutex
	windows := make(map[string]*ipRateWindow)

	// 古いウィンドウのエントリがmapに際限なく溜まらないよう、時々まとめて
	// 掃除する(公開プレビューは未認証で誰でも叩けるエンドポイントのため、
	// アクセス元IPの集合が無制限に増え得る)。
	go func() {
		cleanupTicker := time.NewTicker(10 * time.Minute)
		defer cleanupTicker.Stop()
		for range cleanupTicker.C {
			now := time.Now()
			mu.Lock()
			for ip, w := range windows {
				if now.After(w.windowEnds) {
					delete(windows, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()

		mu.Lock()
		w, ok := windows[ip]
		if !ok || now.After(w.windowEnds) {
			w = &ipRateWindow{windowEnds: now.Add(window)}
			windows[ip] = w
		}
		w.count++
		exceeded := w.count > maxRequests
		mu.Unlock()

		if exceeded {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "リクエストが多すぎます。しばらくしてから再度お試しください"})
			return
		}
		c.Next()
	}
}
