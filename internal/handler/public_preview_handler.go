package handler

import (
	"errors"
	"net/http"
	"time"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// PublicPreviewProxy serves preview.a-kiis.com/{slug}/... requests(単一
// サブドメイン・パスベース公開機能、NextPlan.md フェーズ7)。認証済み
// セッション限定のIDE内蔵プレビュー(PreviewProxy、preview_handler.go)とは
// 違い、有効期限内・同時公開数上限内であれば誰でも(ログイン不要で)閲覧
// できるエンドポイント - この router 自体はcmd/main.goのhostRouterが
// Hostヘッダー"preview.a-kiis.com"宛のリクエストだけをここへ回す形で、
// 通常の/api/v2以下の認証済みAPI群とは完全に別立てになっている
// (rate limit(utils.PerIPRateLimiter)もこのルーターだけに適用される)。
//
// WebSocket Upgrade(HMR等)は特別な実装をしていない - httputil.ReverseProxy
// が"Connection: Upgrade"を検出すると自動でクライアント/バックエンド双方の
// 接続をハイジャックしてバイトを直接中継する(Go標準の組み込み挙動、Go1.12
// 以降)ため、Transportの実体がexecトンネル経由(NewPublicPreviewReverseProxy)
// であっても透過的に機能する。gin.ResponseWriterはhttp.Hijackerを実装して
// おり(内部のResponseWriterへ委譲)、この経路でも問題なくハイジャックできる。
func (h *Handler) PublicPreviewProxy(c *gin.Context) {
	slug := c.Param("slug")

	sandbox, err := db.FindSandboxByPublishSlug(h.DB, slug)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "公開プレビュー", "プレビューの取得に失敗しました", err)
		return
	}
	// 存在しないslugと、期限切れ/停止済みのslugを意図的に区別しない(404で
	// 統一する) - 攻撃者に「有効なslugかどうか」の情報を与えないため。
	if sandbox == nil || !sandbox.PublishExpiresAt.After(time.Now()) {
		c.JSON(http.StatusNotFound, gin.H{"error": "指定されたページは見つからないか、公開期間が終了しています"})
		return
	}

	containerID, err := service.ContainerIDForSession(c.Request.Context(), h.DockerClient, sandbox.UserID, sandbox.CourseID)
	if err != nil {
		status, msg := http.StatusNotFound, "対象のサンドボックスが見つかりません"
		switch {
		case errors.Is(err, service.ErrSandboxNotRunning):
			msg = "対象のサンドボックスは現在停止しています"
		case !errors.Is(err, service.ErrSandboxNotFound):
			status, msg = http.StatusInternalServerError, "プレビュー対象への接続に失敗しました"
		}
		c.JSON(status, gin.H{"error": msg})
		return
	}

	// c.Param("path")はワイルドカードルート(":slug/*path")の残りパスで、
	// 先頭に"/"が付いた形で返る(ginの仕様)。空(未マッチ)ならアプリの
	// ルートを指すよう"/"にしておく - PreviewProxy(preview_handler.go)と
	// 同じ扱い。
	targetPath := c.Param("path")
	if targetPath == "" {
		targetPath = "/"
	}
	basePath := "/" + slug + "/"

	proxy := service.NewPublicPreviewReverseProxy(h.DockerClient, containerID, targetPath, sandbox.PublishPort, basePath, slug)
	proxy.ServeHTTP(c.Writer, c.Request)
}
