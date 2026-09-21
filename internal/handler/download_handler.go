package handler

import (
	"fmt"
	"log"
	"net/http"

	"ai-education/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// DownloadSandboxWorkspaceZip streams the caller's entire sandbox workspace
// as a single ZIP file(「ダウンロードシステム」- ダウンロードボタン押下で
// 全ファイルをZIP化してダウンロードする機能)。<iframe>のプレビューと違い
// これは生徒自身のセキュアな認証コンテキストからの通常のfetch呼び出し
// (Authorizationヘッダー付き、securedFetch経由)を想定している - ブラウザの
// 素のリンク遷移(<a href>)ではAuthorizationヘッダーを付けられないため、
// プレビュー(preview_ticket_service.go)のような専用チケットが別途必要に
// なるところだが、フロント側はfetchでZIPを取得してからBlobとして
// ダウンロードさせる方式(WorkspaceLayout.tsx参照)にすることでその複雑さを
// 避けている。
func (h *Handler) DownloadSandboxWorkspaceZip(c *gin.Context) {
	userID, courseID, ok := h.authorizeSandboxFileQuery(c)
	if !ok {
		return
	}
	containerID, ok := h.sandboxContainerIDOrRespond(c, userID, courseID)
	if !ok {
		return
	}

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="workspace-%d.zip"`, courseID))

	if err := service.StreamSandboxWorkspaceZip(c.Request.Context(), h.DockerClient, containerID, c.Writer); err != nil {
		// ZIPデータの送信を(部分的にでも)開始済みの場合、レスポンスヘッダーは
		// 既に確定して送信されてしまっているため、この時点からJSONエラー
		// レスポンスへ切り替えることはできない(c.Writer.Written()で判定) -
		// 通常のファイルダウンロードAPI全般に共通する制約。ログにだけ残す。
		if !c.Writer.Written() {
			h.respondError(c, http.StatusInternalServerError, "ワークスペースダウンロード", "ダウンロードに失敗しました", err)
			return
		}
		log.Printf("[DOWNLOAD] ワークスペースZIP生成中にエラー container=%s err=%v", containerID, err)
	}
}
