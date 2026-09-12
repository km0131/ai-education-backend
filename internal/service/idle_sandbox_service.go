package service

import (
	"context"
	"errors"
	"log"
	"time"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/docker"

	"gorm.io/gorm"
)

// idleSweepInterval is how often the background sweeper checks for idle
// sandboxes. Unlike SANDBOX_IDLE_TIMEOUT_MINUTES (the idle threshold
// itself), this cadence is an internal implementation detail rather than
// something operators need to tune per deployment - a sandbox can stay idle
// up to idleSweepInterval longer than the configured timeout before it's
// noticed, which is an acceptable margin for this workload.
const idleSweepInterval = 1 * time.Minute

// idleStopTimeout bounds each individual auto-stop's Docker call so one
// unresponsive container can't stall the whole sweep pass.
const idleStopTimeout = 30 * time.Second

// StartIdleSandboxSweeper launches a background goroutine that periodically
// stops (does not delete) running sandboxes nobody has touched in over
// SANDBOX_IDLE_TIMEOUT_MINUTES. NextPlan.md フェーズ2:
// 「アイドルタイムアウトによる自動停止ロジック(公開中のコンテナは対象外)」。
//
// 公開中(Published=true)のコンテナは対象外(フェーズ7)。除外は
// db.ListIdleRunningSandboxes側のpublished=false条件だけで表現しているため、
// 公開機能実装時にこのスイーパー自体を変更する必要はない - 公開開始時に
// Publishedをtrueにする実装さえ入れば、自動的にこのスイーパーの対象から外れる。
func StartIdleSandboxSweeper(cli docker.ContainerAPI, database *gorm.DB) {
	go func() {
		log.Println("[IDLE-SWEEPER-INIT] 🚀 サンドボックスのアイドルタイムアウト監視を起動しました")
		ticker := time.NewTicker(idleSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			sweepIdleSandboxesOnce(cli, database)
		}
	}()
}

// sweepIdleSandboxesOnce runs a single sweep pass. Each sandbox is stopped
// in its own short transaction so that one failure (e.g. a container the
// Docker daemon has already lost track of) doesn't affect the others.
func sweepIdleSandboxesOnce(cli docker.ContainerAPI, database *gorm.DB) {
	cutoff := time.Now().Add(-idleTimeout())

	idle, err := db.ListIdleRunningSandboxes(database, cutoff)
	if err != nil {
		log.Printf("[IDLE-SWEEPER-ERROR] 対象サンドボックスの取得に失敗しました: %v", err)
		return
	}

	for _, sb := range idle {
		ctx, cancel := context.WithTimeout(context.Background(), idleStopTimeout)
		err := database.Transaction(func(tx *gorm.DB) error {
			return StopProgramContainer(ctx, cli, tx, sb.UserID, sb.CourseID)
		})
		cancel()

		if err != nil {
			if errors.Is(err, ErrSandboxNotFound) {
				// 生徒自身がこの直前に停止/削除していた競合。無視してよい。
				continue
			}
			log.Printf("[IDLE-SWEEPER-ERROR] container=%s user=%s course=%d の自動停止に失敗しました: %v", sb.ContainerID, sb.UserID, sb.CourseID, err)
			continue
		}
		log.Printf("[IDLE-SWEEPER] 💤 アイドルタイムアウトによりコンテナを自動停止しました container=%s user=%s course=%d", sb.ContainerID, sb.UserID, sb.CourseID)
	}
}
