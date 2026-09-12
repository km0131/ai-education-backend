package service

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/docker"
	"ai-education/backend/internal/model"

	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"gorm.io/gorm"
)

// リソース使用状況ダッシュボード(教師専用、TeacherDashboardModal.tsx)向けの
// ホスト全体/個別コンテナ/公開枠の計測。MetricsWebSocket(metrics_handler.go)
// が2秒おきにCollectSystemMetricsを呼び、そのままJSONでブロードキャストする。

// HostMetrics はバックエンドプロセス自身が動くホストマシンのCPU/メモリ/
// ディスク使用状況(gopsutil)。
type HostMetrics struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsedMB   float64 `json:"mem_used_mb"`
	MemTotalMB  float64 `json:"mem_total_mb"`
	MemPercent  float64 `json:"mem_percent"`
	DiskUsedGB  float64 `json:"disk_used_gb"`
	DiskTotalGB float64 `json:"disk_total_gb"`
	DiskPercent float64 `json:"disk_percent"`
}

// PublishMetrics is 現在の公開枠使用状況(「🌐 Web公開中: 7 / 10台」)。
type PublishMetrics struct {
	Count int `json:"count"`
	Max   int `json:"max"`
}

// ContainerMetrics is one running sandbox's live resource snapshot(教師
// ダッシュボードの各生徒カード「💻 CPU: 12% | 🧠 RAM: 140MB」)。
type ContainerMetrics struct {
	UserID     uuid.UUID `json:"user_id"`
	CourseID   uint      `json:"course_id"`
	CPUPercent float64   `json:"cpu_percent"`
	MemMB      float64   `json:"mem_mb"`
}

// SystemMetricsSnapshot is one broadcast tick's full payload
// (MetricsWebSocket)。
type SystemMetricsSnapshot struct {
	Host       HostMetrics        `json:"host"`
	Published  PublishMetrics     `json:"published"`
	Containers []ContainerMetrics `json:"containers"`
}

// metricsDiskPath reads METRICS_DISK_PATH from the environment, falling back
// to "/"(ほぼ全環境に存在する安全側のデフォルト) - dockerのデータルート等、
// 実際に監視したいボリュームが別にある場合に運用側で指定できるようにする。
func metricsDiskPath() string {
	if v := os.Getenv("METRICS_DISK_PATH"); v != "" {
		return v
	}
	return "/"
}

// collectHostMetrics reads バックエンドプロセス自身のホストのCPU/メモリ/
// ディスク使用状況(gopsutil)。DOCKER_HOSTがssh://等でリモートを指す構成
// (docker/client.go NewClientのコメント参照)では、これは実際にコンテナが
// 動いているマシンとは別マシンの値になり得る - コンテナ個別の統計は常に
// DOCKER_HOST経由のDocker Engine API(collectContainerStats)から取得する
// ため、そちらはSSH/リモート環境でも正しい値になる。gopsutilの各呼び出しは
// どれか1つが失敗して(パーミッション不足なDevcontainer/一部のマウント権限が
// 無い環境等)も残りを巻き込まないよう、個別にエラーを吸収してログにだけ残す
// (ダッシュボード全体を止めない)。
func collectHostMetrics() HostMetrics {
	var out HostMetrics

	// interval=0: 前回のPercent呼び出しからの経過時間で瞬間値を計算する
	// (gopsutilの仕様) - MetricsWebSocketが2秒おきに呼ぶ運用そのものが
	// このAPIの想定した使い方に合う(間隔を空けて2回計測する必要が無い)。
	if percents, err := cpu.Percent(0, false); err == nil && len(percents) > 0 {
		out.CPUPercent = percents[0]
	} else if err != nil {
		log.Printf("[METRICS] ホストCPU使用率の取得に失敗しました: %v", err)
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		out.MemUsedMB = float64(vm.Used) / (1024 * 1024)
		out.MemTotalMB = float64(vm.Total) / (1024 * 1024)
		out.MemPercent = vm.UsedPercent
	} else {
		log.Printf("[METRICS] ホストメモリ使用率の取得に失敗しました: %v", err)
	}

	if du, err := disk.Usage(metricsDiskPath()); err == nil {
		out.DiskUsedGB = float64(du.Used) / (1024 * 1024 * 1024)
		out.DiskTotalGB = float64(du.Total) / (1024 * 1024 * 1024)
		out.DiskPercent = du.UsedPercent
	} else {
		log.Printf("[METRICS] ホストディスク使用率の取得に失敗しました(path=%s): %v", metricsDiskPath(), err)
	}

	return out
}

// containerCPUPercent replicates the Docker CLI's `docker stats`CPU%計算式
// (cpu_stats/precpu_statsの差分をシステム全体の経過時間で割り、オンライン
// CPU数を掛ける)。ContainerStatsをstream=falseで呼ぶと、Docker Engine自身が
// 内部で短い間隔を空けた2回分のサンプルをcpu_stats/precpu_statsに詰めて
// 返してくれるため、ここで独自にリトライ/間隔待ちをする必要は無い。
// precpu_statsが空(コンテナ起動直後等)でsystemDelta<=0の場合は0を返す
// (割り算エラーを避けるだけで、実害の無いフォールバック)。
func containerCPUPercent(stats container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage) - float64(stats.PreCPUStats.SystemUsage)
	if systemDelta <= 0 || cpuDelta <= 0 {
		return 0
	}

	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}

	return (cpuDelta / systemDelta) * onlineCPUs * 100.0
}

// collectContainerStats fetches one container's live CPU%/メモリ使用量(MB)。
// Docker Engine API経由(DOCKER_HOST、ssh://等リモートでも同じ経路)なので、
// backendプロセス自身が動くホストとは無関係に、実際にコンテナが動いている
// マシンの値を正しく取れる(gopsutilによるホスト全体の計測とは対照的、
// collectHostMetricsのコメント参照)。
func collectContainerStats(ctx context.Context, cli docker.ContainerAPI, containerID string) (cpuPercent, memMB float64, err error) {
	reader, err := cli.ContainerStats(ctx, containerID, false)
	if err != nil {
		return 0, 0, err
	}
	defer reader.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(reader.Body).Decode(&stats); err != nil {
		return 0, 0, err
	}

	return containerCPUPercent(stats), float64(stats.MemoryStats.Usage) / (1024 * 1024), nil
}

// CollectSystemMetrics gathers one snapshot of host + 公開枠 + 個別コンテナの
// 統計(リソース使用状況ダッシュボード、MetricsWebSocketが2秒おきに呼ぶ)。
// 稼働中の各コンテナのContainerStats取得は並列に行う - 直列だとコンテナ数×
// 約1秒(stream=falseの内部サンプリング間隔)かかり、2秒間隔のブロード
// キャストに間に合わなくなるため。1つのコンテナの統計取得に失敗しても
// (停止直後で既に消えている等)、そのコンテナ分がCPU/メモリ0のまま返る
// だけで、他のコンテナ分やhost/公開枠の集計・ブロードキャスト自体は
// 巻き込まない。
func CollectSystemMetrics(ctx context.Context, cli docker.ContainerAPI, database *gorm.DB) (SystemMetricsSnapshot, error) {
	sandboxes, err := db.ListRunningProgramSandboxes(database)
	if err != nil {
		return SystemMetricsSnapshot{}, err
	}
	publishedCount, err := db.CountPublishedSandboxes(database)
	if err != nil {
		return SystemMetricsSnapshot{}, err
	}

	containers := make([]ContainerMetrics, len(sandboxes))
	var wg sync.WaitGroup
	for i, sandbox := range sandboxes {
		wg.Add(1)
		go func(i int, sandbox model.ProgramSandbox) {
			defer wg.Done()
			cpuPercent, memMB, statErr := collectContainerStats(ctx, cli, sandbox.ContainerID)
			if statErr != nil {
				log.Printf("[METRICS] container=%s の統計取得に失敗しました: %v", sandbox.ContainerID, statErr)
			}
			containers[i] = ContainerMetrics{
				UserID:     sandbox.UserID,
				CourseID:   sandbox.CourseID,
				CPUPercent: cpuPercent,
				MemMB:      memMB,
			}
		}(i, sandbox)
	}
	wg.Wait()

	return SystemMetricsSnapshot{
		Host: collectHostMetrics(),
		Published: PublishMetrics{
			Count: int(publishedCount),
			Max:   PublishMaxConcurrent(),
		},
		Containers: containers,
	}, nil
}
