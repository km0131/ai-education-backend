package handler

import (
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"

	"ai-education/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// issuePreviewTicketRequest is the body for POST /program/container/preview-ticket.
type issuePreviewTicketRequest struct {
	CourseID uint `json:"course_id" binding:"required"`
}

// IssuePreviewTicket issues a short-lived, reusable ticket
// (IssuePreviewTicket、preview_ticket_service.go)that authenticates the
// iframe-based Web preview proxy below(PreviewProxy) - <iframe src=...>では
// Authorizationヘッダーを付けられないため、シェル/LSPと同じ理由でクエリ/
// パス経由のチケット認証にする。ただし1ページの表示で何本もリクエストが
// 飛ぶ(HTML+CSS+JS+画像等)ため、シェル/LSPの使い切りチケットとは違い、
// 有効期限内は使い回せる(IDE内蔵Webプレビュー、VS CodeのSimple Browser相当)。
func (h *Handler) IssuePreviewTicket(c *gin.Context) {
	var req issuePreviewTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "リクエストが不正です"})
		return
	}
	userID, ok := h.authorizeSandboxCourse(c, req.CourseID)
	if !ok {
		return
	}

	ticket, expiresAt, err := service.IssuePreviewTicket(userID, req.CourseID)
	if err != nil {
		h.respondError(c, http.StatusInternalServerError, "プレビューチケット発行", "チケットの発行に失敗しました", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_at": expiresAt})
}

// PreviewProxy reverse-proxies an authenticated student's own requests to an
// arbitrary port inside their own sandbox container(「IDE内蔵Webプレビュー」、
// VS CodeのSimple Browser相当、NextPlan.md)。NextPlan.mdフェーズ7の外部公開
// 機能(ワイルドカードDNS/Cloudflare Tunnel経由で他者にも見せる公開URL)とは
// 別物 - こちらは常に本人の認証済みセッションからしか到達できない、開発中の
// プレビュー専用で、悪用対策(公開期間・コンテンツスキャン等)は不要。
//
// シェル/LSPのWebSocketと同じ理由でAuthMiddlewareの外に置き、
// IssuePreviewTicketが発行したチケットで認証する(ワンタイムではなく、
// 有効期限内は使い回せる - preview_ticket_service.go参照。1ページの表示で
// HTML本体・CSS・JS・画像など何本ものリクエストが飛ぶため)。
//
// 実際の中継はコンテナのブリッジネットワークIPへ直接HTTP接続するのでは
// なく、シェル/LSPと同じexec経由(StartPreviewTunnel、preview_service.go)
// で行う - backendサービス自体はsandbox-netに参加していない
// (docker-compose.yml、NextPlan.md §3.3のネットワーク分離が必須要件の
// ため)。
func (h *Handler) PreviewProxy(c *gin.Context) {
	ticket := c.Param("ticket")
	userID, courseID, ok := service.ValidatePreviewTicket(ticket)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが無効か期限切れです"})
		return
	}

	portStr := c.Param("port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ポート番号が不正です"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, courseID)
	if !ok {
		return
	}

	// c.Param("path")はワイルドカードルート(":ticket/:port/*path")の残り
	// パスで、先頭に"/"が付いた形で返る(ginの仕様)。空(未マッチ)なら
	// アプリのルートを指すよう"/"にしておく。
	targetPath := c.Param("path")
	if targetPath == "" {
		targetPath = "/"
	}
	basePath := "/api/v2/program/container/preview/" + ticket + "/" + portStr + "/"

	proxy := service.NewPreviewReverseProxy(h.DockerClient, containerID, targetPath, port, basePath)
	proxy.ServeHTTP(c.Writer, c.Request)
}

// PreviewFile serves a single file's raw bytes directly from the caller's
// own sandbox workspace(「実行」ボタンの.html拡張、NextPlan.md) - 静的な
// .html/.css/.js/画像などを、コンテナ内でサーバープロセスを1つも起動せず
// プレビューできるようにする(PreviewProxyがポートへ中継するのに対し、
// こちらはファイルシステムから直接読む)。
//
// ワイルドカードの残りパスをそのままワークスペース相対パスとして解決する
// ため、HTMLが`<link href="style.css">`のように相対パスで参照している
// 同じディレクトリのアセットは、ブラウザの通常の相対URL解決でそのまま
// 正しいURLになる(絶対パス(先頭"/")の参照はこのAPI自身のルートを指して
// しまうため対象外 - シンプルな相対パス構成の静的ページ向け、
// NewPreviewReverseProxyの制約と同じ考え方)。
func (h *Handler) PreviewFile(c *gin.Context) {
	ticket := c.Param("ticket")
	userID, courseID, ok := service.ValidatePreviewTicket(ticket)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "チケットが無効か期限切れです"})
		return
	}

	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, courseID)
	if !ok {
		return
	}

	filePath, err := service.ResolveSandboxRelativePath(c.Param("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "パスが不正です"})
		return
	}

	data, err := service.ReadSandboxFileBytes(c.Request.Context(), h.DockerClient, containerID, filePath)
	switch {
	case errors.Is(err, service.ErrSandboxPathNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "ファイルが見つかりません"})
		return
	case errors.Is(err, service.ErrSandboxPathIsDirectory):
		c.JSON(http.StatusBadRequest, gin.H{"error": "指定されたパスはディレクトリです"})
		return
	case errors.Is(err, service.ErrSandboxFileTooLarge):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ファイルサイズが大きすぎます"})
		return
	case err != nil:
		h.respondError(c, http.StatusInternalServerError, "静的プレビュー", "ファイルの取得に失敗しました", err)
		return
	}

	contentType := mime.TypeByExtension(path.Ext(filePath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Data(http.StatusOK, contentType, data)
}
