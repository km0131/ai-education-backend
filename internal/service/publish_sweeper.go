package service

import (
	"log"
	"time"

	"ai-education/backend/internal/db"

	"gorm.io/gorm"
)

// publishSweepInterval is how often the background sweeper checks for
// expired publishes(タスク指示書「1分ごとにバックグラウンド処理を走らせ」)。
// idleSweepInterval(idle_sandbox_service.go)と同じ考え方で、この値自体は
// 運用で調整する対象ではない(調整対象はPUBLISH_DEFAULT_DURATION_HOURS)。
const publishSweepInterval = 1 * time.Minute

// StartPublishExpirySweeper launches a background goroutine that
// periodically downgrades expired publishes(Published=false)and removes
// them from the effective routing table - FindSandboxByPublishSlug
// (program_sandbox.go)はpublished=trueの行しか返さないため、この
// スイーパーがフラグを降ろすだけでPublicPreviewProxy(public_preview_handler.go)
// からは即座に見えなくなる(削除される行はProgramSandbox自体ではなく、
// 「公開状態」というフラグだけ)。
func StartPublishExpirySweeper(database *gorm.DB) {
	go func() {
		log.Println("[PUBLISH-SWEEPER-INIT] 🚀 公開期限の自動失効監視を起動しました")
		ticker := time.NewTicker(publishSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			sweepExpiredPublishesOnce(database)
		}
	}()
}

// sweepExpiredPublishesOnce runs a single sweep pass.
func sweepExpiredPublishesOnce(database *gorm.DB) {
	expired, err := db.ListExpiredPublishedSandboxes(database, time.Now())
	if err != nil {
		log.Printf("[PUBLISH-SWEEPER-ERROR] 対象サンドボックスの取得に失敗しました: %v", err)
		return
	}

	for _, sb := range expired {
		if err := db.UnpublishProgramSandbox(database, sb.UserID, sb.CourseID); err != nil {
			log.Printf("[PUBLISH-SWEEPER-ERROR] user=%s course=%d の公開自動失効に失敗しました: %v", sb.UserID, sb.CourseID, err)
			continue
		}
		log.Printf("[PUBLISH-SWEEPER] ⏱ 公開期限切れのため自動的に非公開にしました user=%s course=%d slug=%s", sb.UserID, sb.CourseID, sb.PublishSlug)
	}
}
