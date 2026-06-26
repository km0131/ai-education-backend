package api

import (
	"ai-education/backend/internal/model"
	"bytes"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/goccy/go-json"
)

// Python API呼び出し用関数 画像評価
func CallPythonAnalysisAPI(photoID uint, path string) (*model.AnalysisData, error) {
	apiURL := os.Getenv("PYTHON_ANALYSIS_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:5000/analyze"
	}
	// 1. ローカルの画像ファイルを開く
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open image file: %v", err)
	}
	defer file.Close()
	// 2. マルチパートのボディを組み立てる
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	// 🌟 Python側が受け取る引数名「file」と完全に一致させる
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	// 画像データをコピー
	if _, err = io.Copy(part, file); err != nil {
		return nil, err
	}
	// PhotoID もフォームデータとして一緒に送る場合（必要に応じて）
	_ = writer.WriteField("id", strconv.FormatUint(uint64(photoID), 10))
	// 忘れずにクローズしてバウンダリを確定させる
	if err := writer.Close(); err != nil {
		return nil, err
	}
	// 3. リクエストの送信
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", apiURL, body)
	if err != nil {
		return nil, err
	}
	// 🌟 Content-Type に境界文字列（boundary）を含めるのが必須
	req.Header.Set("Content-Type", writer.FormDataContentType())
	pythonSecret := os.Getenv("PYTHON_API_SECRET")
	if pythonSecret == "" {
		pythonSecret = "secure_python_analyze_secret_token_abc" // Python側のデフォルトと合わせる
	}
	req.Header.Set("Authorization", "Bearer "+pythonSecret)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api error: status %d", resp.StatusCode)
	}
	// 4. レスポンスのデコード
	var result model.AnalysisData
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendTrainingZipToGCP は作成したZIPファイルをGCP上のPython /process APIへ送信します
func SendTrainingZipToGCP(jobID uint, zipPath string) error {
	// 1. 環境変数からGCPのAPI URLを取得（なければローカルをデフォルトに）
	apiURL := os.Getenv("GCP_AI_TRAINING_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8000/process" // GCPインスタンスのFastAPIポートに合わせて調整してください
	}

	// 2. 作成したZIPファイルを開く
	file, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip file: %w", err)
	}
	defer file.Close()

	// マルチパートのボディを組み立てる
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Python側のFastAPIが受け取る引数名（例: "file"）と一致させる
	part, err := writer.CreateFormFile("file", filepath.Base(zipPath))
	if err != nil {
		return fmt.Errorf("failed to create multipart form file: %w", err)
	}

	// ZIPデータをコピー
	if _, err = io.Copy(part, file); err != nil {
		return fmt.Errorf("failed to copy zip data: %w", err)
	}

	// JobID もフォームデータ（"job_id"）として一緒に送信する
	err = writer.WriteField("job_id", strconv.FormatUint(uint64(jobID), 10))
	if err != nil {
		return fmt.Errorf("failed to write job_id field: %w", err)
	}

	// クローズしてバウンダリ（境界線）を確定
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// リクエストの送信
	// 🌟 重要: AI学習の通信と処理には時間がかかるため、タイムアウトを30分に設定
	client := &http.Client{Timeout: 30 * time.Minute}
	req, err := http.NewRequest("POST", apiURL, body)
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}

	// Content-Type に境界文字列（boundary）を含める
	req.Header.Set("Content-Type", writer.FormDataContentType())
	pythonSecret := os.Getenv("PYTHON_API_SECRET")
	if pythonSecret == "" {
		pythonSecret = "secure_python_analyze_secret_token_abc" // Python側のデフォルトと合わせる
	}
	req.Header.Set("Authorization", "Bearer "+pythonSecret)

	resp, err := client.Do(req)

	log.Printf("[INFO] GCPのAIサーバーへZIPを送信中... URL: %s", apiURL)
	resp, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to GCP: %w", err)
	}
	defer resp.Body.Close()

	// ステータスチェック
	if resp.StatusCode != http.StatusOK {
		// エラー内容がわかるようにボディを少し読み出す
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gcp api error: status %d, body: %s", resp.StatusCode, string(errorBody))
	}

	log.Printf("[INFO] GCPへのZIP送信およびAI作成リクエストが正常に受け付けられました（JobID: %d）", jobID)
	return nil
}
