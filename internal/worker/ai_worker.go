package worker

import (
	"ai-education/backend/internal/api"
	"ai-education/backend/internal/db" // パッケージを正しくインポート
	"log"
	"strconv"

	"gorm.io/gorm"
)

var AnalysisQueue = make(chan uint, 100)

func StartAnalysisWorker(database *gorm.DB) {
	if database == nil {
		log.Println("[WORKER-FATAL] 🚨 database インスタンスが nil です。分析ワーカーを起動できません。")
		return
	}
	go func() {
		log.Println("[WORKER-INIT] 🚀 AI分析バックグラウンドワーカーが正常に起動しました。キューの監視を開始します。")
		for photoID := range AnalysisQueue {
			log.Printf("[WORKER] 📥 キューを受信: PhotoID = %d", photoID)
			// 1. DBから画像情報を取得
			photo, err := db.GetPhotographByID(database, strconv.Itoa(int(photoID)))
			if err != nil {
				log.Printf("[WORKER-ERROR] ❌ DBからの画像取得に失敗 (PhotoID: %d): %v", photoID, err)
				continue
			}
			log.Printf("[WORKER] 🛰️ Python APIへ解析リクエストを送信中... (PhotoID: %d, Path: %s)", photo.ID, photo.PhotographPath)
			// 2. Python APIに画像を送信して解析依頼
			analysisData, err := api.CallPythonAnalysisAPI(photo.ID, photo.PhotographPath)
			if err != nil {
				// エラーの具体内容（接続拒否、タイムアウトなど）を明示
				log.Printf("[WORKER-ERROR] ❌ Python API解析リクエストが失敗 (PhotoID: %d): %v", photo.ID, err)
				continue
			}
			log.Printf("[WORKER] 💾 Pythonからの解析結果をDBに反映中... (PhotoID: %d)", photo.ID)
			// 3. 解析結果をDBに更新
			err = db.UpdatePhotoAnalysis(database, int(photo.ID), *analysisData)
			if err != nil {
				log.Printf("[WORKER-ERROR] ❌ DBへの解析結果更新に失敗 (PhotoID: %d): %v", photo.ID, err)
				continue
			}
			log.Printf("[WORKER] ✅ 写真の解析・同期がすべて完了しました！ (PhotoID: %d)", photo.ID)
		}
	}()
}

// Jobの構造体を流すキュー（容量は余裕を持って100など）
var TrainJobQueue = make(chan uint, 100)

func StartTrainWorker(database *gorm.DB) {
	if database == nil {
		log.Println("[WORKER-FATAL] 🚨 ai インスタンスが nil です。分析ワーカーを起動できません。")
		return
	}
	// ワーカースレッドは「1つ」だけ起動（絶対に同時学習させない）
	go func() {
		log.Println("[WORKER-INIT] 🚀 AI作成バックグラウンドワーカーが正常に起動しました。キューの監視を開始します。")
		for jobID := range TrainJobQueue {
			log.Printf("[TRAIN-WORKER] 🚀 ジョブの処理を開始: JobID = %d", jobID)

			// ステータスを "training" に変更
			status, err := db.ChangeStatus(database, strconv.Itoa(int(jobID)))
			if err != nil {
				log.Printf("[WORKER-ERROR] ❌ DBからの画像取得に失敗 (JobID: %d): %v", jobID, err)
				continue
			}
			log.Printf("[WORKER] ステータスをtrainingに変更(JobID: %d)", jobID)

			// スナップショットから画像とラベルのマップを収集
			trainingData, err := db.FetchTrainingDataByJobID(database, status.ID)
			if err != nil {
				log.Printf("[ERROR] データ収集失敗: %v", err)
				// 必要に応じてここでステータスを "failed" に落とす処理を入れる
				continue
			}

			// 収集した trainingData を元にZIPファイルを組み立てる
			// trainingData は map[int][]string なので、どのラベルに何の画像があるか一目瞭然です
			zipPath, err := CreateTrainingZip(trainingData, jobID)
			if err != nil {
				log.Printf("[ERROR] ZIP作成失敗: %v", err)
				continue
			}
			// Pythonの /process (または /train) API へ送信
			err = api.SendTrainingZipToGCP(jobID, zipPath)
			if err != nil {
				// エラーの具体内容（接続拒否、タイムアウトなど）を明示
				log.Printf("[WORKER-ERROR] ❌ Python API解析リクエストが失敗 (PhotoID: %d): %v", jobID, err)
				continue
			}

			log.Printf("[TRAIN-WORKER] ✅ ジョブが完了しました: JobID = %d", jobID)
		}
	}()
}
