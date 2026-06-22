package worker

import (
	"ai-education/backend/internal/db" // パッケージを正しくインポート
	"ai-education/backend/internal/model"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

var AnalysisQueue = make(chan uint, 100)

func StartAnalysisWorker() {
	go func() {
		for photoID := range AnalysisQueue {
			// 1. DBから画像情報を取得
			photo, _ := db.GetPhotographByID(strconv.Itoa(int(photoID)))

			// 2. Python APIに画像を送信して解析依頼
			analysisData, err := callPythonAnalysisAPI(photo.ID, photo.PhotographPath)
			if err != nil {
				continue // エラーハンドリング
			}

			// 3. 解析結果をDBに更新
			err = db.UpdatePhotoAnalysis(int(photo.ID), *analysisData)
			if err != nil {
				log.Printf("Failed to update DB for photo ID %d: %v", photo.ID, err)
				continue
			}
		}
	}()
}

// Python API呼び出し用関数
func callPythonAnalysisAPI(photoID uint, path string) (*model.AnalysisData, error) {
	// .env から URL を取得 (デフォルト値を設定しておくと安全)
	apiURL := os.Getenv("PYTHON_ANALYSIS_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:5000/analyze" // フォールバック
	}
	//送信するペイロードを作成
	payload := map[string]string{
		"path": path,
		"id":   strconv.FormatUint(uint64(photoID), 10),
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// --- 修正案: タイムアウトを設けたクライアントを使用 ---
	client := &http.Client{
		Timeout: 30 * time.Second, // 30秒以内に結果が返らない場合はエラーにする
	}
	// POSTリクエストの実行
	resp, err := client.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 4. レスポンスのデコード
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api error: status %d", resp.StatusCode)
	}

	var result model.AnalysisData
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}
