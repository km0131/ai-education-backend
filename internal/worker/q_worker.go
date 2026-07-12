package worker

import (
	"ai-education/backend/internal/api"
	"ai-education/backend/internal/db"
	"container/heap"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GPUJobKind はジョブの種類
type GPUJobKind string

const (
	JobKindTrain    GPUJobKind = "train"
	JobKindTest     GPUJobKind = "test"
	JobKindAnalysis GPUJobKind = "analysis"
)

// 優先度（数値が小さいほど先に処理される）
const (
	PriorityTrain    = 10
	PriorityTest     = 15
	PriorityAnalysis = 20
)

// GPUJobRequest: GPUを使う処理（学習/テスト/画像分析）を1本のキューに集約するためのジョブ定義
// GPUは同時に1系統しか処理できないため、種別を問わずすべてこの構造体でスケジューリングする
type GPUJobRequest struct {
	Kind     GPUJobKind
	Priority int
	index    int // heapが内部で使うインデックス（利用者は触らない）

	// JobKindTrain で使用
	JobID uint

	// JobKindTest で使用
	StatusID  uint
	ProjectID uuid.UUID
	CourseID  uint

	// JobKindAnalysis で使用
	PhotoID uint
}

// 優先度付きキュー本体（container/heapのインタフェース実装）
type gpuPriorityQueue []*GPUJobRequest

func (pq gpuPriorityQueue) Len() int { return len(pq) }
func (pq gpuPriorityQueue) Less(i, j int) bool {
	return pq[i].Priority < pq[j].Priority // 数値が小さいほど先に処理
}
func (pq gpuPriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}
func (pq *gpuPriorityQueue) Push(x any) {
	item := x.(*GPUJobRequest)
	item.index = len(*pq)
	*pq = append(*pq, item)
}
func (pq *gpuPriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*pq = old[:n-1]
	return item
}

// GPUScheduler: 優先度付きキューをスレッドセーフに扱うラッパー
type GPUScheduler struct {
	mu    sync.Mutex
	cond  *sync.Cond
	queue gpuPriorityQueue
}

func NewGPUScheduler() *GPUScheduler {
	s := &GPUScheduler{queue: make(gpuPriorityQueue, 0)}
	s.cond = sync.NewCond(&s.mu)
	heap.Init(&s.queue)
	return s
}

// Enqueue はジョブをキューに追加する（優先度順に自動整列）
func (s *GPUScheduler) Enqueue(job *GPUJobRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	heap.Push(&s.queue, job)
	s.cond.Signal() // 待機中のワーカーを起こす
}

// Dequeue は最も優先度の高いジョブを取り出す（無ければブロックして待つ）
func (s *GPUScheduler) Dequeue() *GPUJobRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.queue.Len() == 0 {
		s.cond.Wait()
	}
	return heap.Pop(&s.queue).(*GPUJobRequest)
}

var Scheduler = NewGPUScheduler()

// StartGPUWorker: PythonへのGPU処理を1件ずつ直列に実行する唯一のワーカー
func StartGPUWorker(database *gorm.DB) {
	go func() {
		log.Println("[GPU-WORKER-INIT] 🚀 GPUジョブスケジューラを起動しました")
		for {
			job := Scheduler.Dequeue()
			log.Printf("[GPU-WORKER] 📥 ジョブ実行開始: kind=%s, status_id=%d, priority=%d", job.Kind, job.StatusID, job.Priority)

			switch job.Kind {
			case JobKindTrain:
				handleTrainJob(database, job)
			case JobKindTest:
				handleTestJob(database, job)
			case JobKindAnalysis:
				handleAnalysisJob(database, job)
			default:
				log.Printf("[GPU-WORKER-ERROR] 未知のジョブ種別: %s", job.Kind)
			}
		}
	}()
}

func handleTestJob(database *gorm.DB, job *GPUJobRequest) {
	zipPath, err := TestExecutionWorker(database, int(job.StatusID), job.ProjectID, job.CourseID)
	if err != nil {
		log.Printf("[WORKER-ERROR] ❌ テスト用ZIPの作成に失敗 (StatusID: %d): %v", job.StatusID, err)
		return
	}
	if err := api.SendTestZipToPython(job.StatusID, zipPath); err != nil {
		log.Printf("[WORKER-ERROR] ❌ Python APIへのテスト実行リクエストが失敗 (StatusID: %d): %v", job.StatusID, err)
	}
}

func handleTrainJob(database *gorm.DB, job *GPUJobRequest) {
	// ステータスを "training" に変更
	status, err := db.ChangeStatus(database, strconv.Itoa(int(job.JobID)))
	if err != nil {
		log.Printf("[WORKER-ERROR] ❌ DBからのジョブ取得に失敗 (JobID: %d): %v", job.JobID, err)
		return
	}
	log.Printf("[TRAIN-WORKER] ステータスをtrainingに変更(JobID: %d)", job.JobID)
	buildStart := time.Now()
	// スナップショットから画像とラベルのマップを収集
	trainingData, err := db.FetchTrainingDataByJobID(database, status.ID)
	if err != nil {
		log.Printf("[TRAIN-WORKER-ERROR] ❌ データ収集失敗 (JobID: %d): %v", job.JobID, err)
		return
	}
	log.Printf("[PROFILE] trainingData 組み立て所要時間: %v (JobID: %d)", time.Since(buildStart), job.JobID)

	// 収集した trainingData を元にZIPファイルを組み立てる
	zipPath, err := CreateTrainingZip(trainingData, job.JobID)
	if err != nil {
		log.Printf("[TRAIN-WORKER-ERROR] ❌ ZIP作成失敗 (JobID: %d): %v", job.JobID, err)
		return
	}

	// Pythonの /process API へ送信
	if err := api.SendTrainingZipToGCP(job.JobID, zipPath); err != nil {
		log.Printf("[TRAIN-WORKER-ERROR] ❌ Python APIへの学習リクエストが失敗 (JobID: %d): %v", job.JobID, err)
		return
	}

	log.Printf("[TRAIN-WORKER] ✅ ジョブが完了しました: JobID = %d", job.JobID)
}

func handleAnalysisJob(database *gorm.DB, job *GPUJobRequest) {
	// DBから画像情報を取得
	photo, err := db.GetPhotographByID(database, strconv.Itoa(int(job.PhotoID)))
	if err != nil {
		log.Printf("[ANALYSIS-WORKER-ERROR] ❌ DBからの画像取得に失敗 (PhotoID: %d): %v", job.PhotoID, err)
		return
	}

	log.Printf("[ANALYSIS-WORKER] 🛰️ Python APIへ解析リクエストを送信中... (PhotoID: %d, Path: %s)", photo.ID, photo.PhotographPath)
	// Python APIに画像を送信して解析依頼
	analysisData, err := api.CallPythonAnalysisAPI(photo.ID, photo.PhotographPath)
	if err != nil {
		log.Printf("[ANALYSIS-WORKER-ERROR] ❌ Python API解析リクエストが失敗 (PhotoID: %d): %v", photo.ID, err)
		return
	}

	// 解析結果をDBに更新
	if err := db.UpdatePhotoAnalysis(database, int(photo.ID), *analysisData); err != nil {
		log.Printf("[ANALYSIS-WORKER-ERROR] ❌ DBへの解析結果更新に失敗 (PhotoID: %d): %v", photo.ID, err)
		return
	}

	log.Printf("[ANALYSIS-WORKER] ✅ 写真の解析・同期がすべて完了しました！ (PhotoID: %d)", photo.ID)
}
