package api

import (
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
)

// SendTestZipToPython は生成したテスト実行用ZIP（テスト画像・モデル・結果用JSON）をPythonの推論APIへ送信します
// SendTestZipToPython は生成したテスト実行用ZIP（テスト画像・モデル・結果用JSON）をPythonの推論APIへ送信します
// 処理結果は後でPython側から別途Webhook（受け取り用API）経由で送られてくるため、ここでは送信の受理確認のみ行う
func SendTestZipToPython(statusID uint, zipPath string) error {
	apiURL := os.Getenv("PYTHON_TEST_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8000/test"
	}

	file, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip file: %w", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file_zip", filepath.Base(zipPath))
	if err != nil {
		return fmt.Errorf("failed to create multipart form file: %w", err)
	}
	if _, err = io.Copy(part, file); err != nil {
		return fmt.Errorf("failed to copy zip data: %w", err)
	}

	if err := writer.WriteField("status_id", strconv.FormatUint(uint64(statusID), 10)); err != nil {
		return fmt.Errorf("failed to write status_id field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// 受理確認だけなので、タイムアウトは短めでよい（アップロード自体の時間だけ見込めばOK）
	client := &http.Client{Timeout: 1 * time.Minute}
	req, err := http.NewRequest("POST", apiURL, body)
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	pythonSecret := os.Getenv("PYTHON_API_SECRET")
	if pythonSecret == "" {
		pythonSecret = "secure_python_analyze_secret_token_abc"
	}
	req.Header.Set("Authorization", "Bearer "+pythonSecret)

	log.Printf("[INFO] Pythonのテスト実行APIへZIPを送信中... URL: %s", apiURL)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to python: %w", err)
	}
	defer resp.Body.Close()

	// 受理された（202 Accepted想定）かどうかだけ確認。テスト完了を待たない。
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("python test api error: status %d, body: %s", resp.StatusCode, string(errorBody))
	}

	log.Printf("[INFO] Pythonへのテスト実行リクエストが正常に受け付けられました（StatusID: %d）", statusID)
	return nil
}
