package handler

import (
	"log"
	"net/http"
	"time"

	"ai-education/backend/internal/service"
	"ai-education/backend/internal/utils"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// リソース使用状況ダッシュボード(教師専用、TeacherDashboardModal.tsx)。
// ホスト全体のCPU/メモリ/ディスク、公開枠の使用状況、稼働中の各生徒
// コンテナのCPU%/メモリ使用量を2秒おきにまとめてブロードキャストする
// (内訳の計測自体はmetrics_service.go参照)。

var metricsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

const metricsBroadcastInterval = 2 * time.Second

// IssueMetricsTicket issues a short-lived, one-time ticket authenticating the
// resource-metrics WebSocket(POST /api/v2/admin/metrics/ticket) - シェル/LSP/
// ライブセッションと同じ理由(ブラウザのWebSocket APIはAuthorizationヘッダーを
// 付けられない)で、PASETO済みのこのエンドポイントから先にticketを取得し、
// WebSocket接続時はticketのみで認証する。
func (h *Handler) IssueMetricsTicket(c *gin.Context) {
	uid, authed := utils.GetUserID(c)
	if !authed {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "認証エラー"})
		return
	}
	isTeacher, ok := utils.GetUserTeacher(c)
	if !ok || !isTeacher {
		c.JSON(http.StatusForbidden, gin.H{"error": "先生以外はこの操作を行えません"})
		return
	}

	ticket, expiresAt, err := service.IssueMetricsTicket(uid)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "チケット発行", "チケットの発行に失敗しました", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_at": expiresAt})
}

// MetricsWebSocket streams host/コンテナ個別/公開枠のリソース使用状況
// スナップショットを2秒おきにブロードキャストする(GET /api/v2/admin/metrics/ws、
// IssueMetricsTicketが発行したticketで認証する - コース/生徒を問わない
// システム全体の計測のため、既存のシェル/LSP/ライブセッションのチケットとは
// 別の専用チケットを使う)。
func (h *Handler) MetricsWebSocket(c *gin.Context) {
	ticketID := c.Query("ticket")
	if _, ok := service.ConsumeMetricsTicket(ticketID); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが無効です"})
		return
	}

	conn, err := metricsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// このWSはサーバー→クライアントの一方向配信のみだが、クライアント切断
	// (ブラウザがタブを閉じた等)を検知するためだけに読み取りループを1本
	// 回しておく - 読み取りに失敗した時点で相手がいなくなったとみなし、
	// 下のブロードキャストループを止める。
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(metricsBroadcastInterval)
	defer ticker.Stop()

	ctx := c.Request.Context()
	for {
		snapshot, err := service.CollectSystemMetrics(ctx, h.DockerClient, h.DB)
		if err != nil {
			log.Printf("[METRICS] スナップショットの収集に失敗しました: %v", err)
		} else if err := conn.WriteJSON(snapshot); err != nil {
			return
		}

		select {
		case <-ticker.C:
		case <-closed:
			return
		case <-ctx.Done():
			return
		}
	}
}
