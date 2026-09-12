package service

import (
	"context"
	"log"
	"time"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/docker"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"gorm.io/gorm"
)

// ReconcileTimeout bounds the whole startup reconciliation pass. Without a
// deadline, a caller passing context.Background() would let a hung Docker
// connection (most notably DOCKER_HOST=ssh://... once the backend and dockerd
// are on physically separate machines, NextPlan.md §3.5 - a TCP/SSH handshake
// to an unreachable or slow-to-answer host can hang far longer than a local
// unix socket ever would) block main() before r.Run() is ever reached,
// i.e. the whole backend - unrelated features included - fails to start.
// Reconciliation is a self-healing nicety, not something worth stalling
// startup over indefinitely for.
const ReconcileTimeout = 30 * time.Second

// ReconcileSandboxState is run once at backend startup, before the server
// starts accepting traffic. Docker itself is the source of truth for a
// sandbox's existence/state - program_sandboxes is only a mirror of it (see
// the ProgramSandbox struct comment in db_models.go) - so on every boot this
// walks both sides once and makes the DB match Docker reality.
//
// バックエンドプロセス自体の再起動は dockerd と分離しているため通常は無害
// (NewClient参照)だが、以下のケースでDBとコンテナ実体がズレうる:
//   - 直前のプロセスが「Docker操作成功→DB書き込み」の間でkillされた
//     (電源断・OOM Killer・デプロイ時のプロセス強制終了 等)
//   - host/dockerd自体が再起動した(コンテナにRestartPolicy未設定のため、
//     dockerd起動時にコンテナは自動復帰しない)
//
// いずれの場合も「DB上はrunningだがDocker側の実体は無い/停止済み」という
// 幽霊runningが生まれうる。特にプールスロットの空き判定(allocatePoolSlot)は
// DB側だけを見て「running かつ pool_slot!=”」の行を使用中とみなすため、
// 幽霊runningを放置すると同じスロットが別の生徒に再割り当てされ、
// workspaceを共有してしまう事故になる - このチェックはそれを防ぐのが主目的。
func ReconcileSandboxState(ctx context.Context, cli docker.ContainerAPI, database *gorm.DB) {
	log.Println("[RECONCILE] 🔍 DB/コンテナ状態の整合性チェックを開始します")

	dockerContainers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "app=program-sandbox")),
	})
	if err != nil {
		log.Printf("[RECONCILE-ERROR] Dockerコンテナ一覧の取得に失敗しました。整合性チェックをスキップします: %v", err)
		return
	}
	byID := make(map[string]container.Summary, len(dockerContainers))
	for _, c := range dockerContainers {
		byID[c.ID] = c
	}

	sandboxes, err := db.ListActiveProgramSandboxes(database)
	if err != nil {
		log.Printf("[RECONCILE-ERROR] DBのサンドボックス一覧の取得に失敗しました。整合性チェックをスキップします: %v", err)
		return
	}

	matched := make(map[string]bool, len(sandboxes))
	for _, sb := range sandboxes {
		c, ok := byID[sb.ContainerID]
		if !ok {
			// Docker側に実体が無い幽霊running。プールスロットの二重割り当て
			// 事故を防ぐため必ずstoppedへ倒す。workspaceはwipeできない
			// (コンテナが既に存在しないため)ので、スロット自体の自動解放は
			// せず安全側に倒す - 必要ならログを見て手動でクリーンアップする。
			if sb.Status != "stopped" {
				if err := db.UpdateProgramSandboxStatus(database, sb.UserID, sb.CourseID, sb.ContainerID, "stopped"); err != nil {
					log.Printf("[RECONCILE-ERROR] status補正に失敗しました user=%s course=%d: %v", sb.UserID, sb.CourseID, err)
					continue
				}
			}
			log.Printf("[RECONCILE] ⚠️ Docker側に実体の無いコンテナのDB記録をstoppedへ補正しました(要確認: プールスロット%sのworkspaceが未wipeの可能性があります) user=%s course=%d container=%s", sb.PoolSlot, sb.UserID, sb.CourseID, sb.ContainerID)
			continue
		}
		matched[sb.ContainerID] = true

		actual := "stopped"
		if c.State == "running" {
			actual = "running"
		}
		if sb.Status != actual {
			if err := db.UpdateProgramSandboxStatus(database, sb.UserID, sb.CourseID, sb.ContainerID, actual); err != nil {
				log.Printf("[RECONCILE-ERROR] status補正に失敗しました user=%s course=%d: %v", sb.UserID, sb.CourseID, err)
				continue
			}
			log.Printf("[RECONCILE] 🔧 DB記録をDocker実体の状態(%s)に補正しました user=%s course=%d container=%s", actual, sb.UserID, sb.CourseID, sb.ContainerID)
		}
	}

	// DBに記録の無い孤立コンテナ(例: 起動シーケンスがDB書き込み直前で中断
	// された)。プールスロットのbind mountを掴んだまま残ると、allocatePoolSlot
	// がDB側だけを見て同じスロットを次の生徒に割り当ててしまい、workspaceを
	// 共有する事故につながるため強制削除する(StartProgramContainerがDB書き込み
	// 失敗時に自分自身のコンテナをロールバック削除するのと同じ方針)。
	for id, c := range byID {
		if matched[id] {
			continue
		}
		if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
			log.Printf("[RECONCILE-ERROR] 孤立コンテナの削除に失敗しました(手動確認が必要です) container=%s names=%v err=%v", id, c.Names, err)
			continue
		}
		log.Printf("[RECONCILE] 🗑️ DB記録の無い孤立コンテナを削除しました container=%s names=%v", id, c.Names)
	}

	log.Println("[RECONCILE] ✅ 整合性チェックが完了しました")
}
