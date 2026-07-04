package worker

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CreateTestingZip はテストに必要なリソース（画像、3モデル、結果用JSON）を1つのZIPにまとめます
func TestExecutionWorker(data *gorm.DB, status int, projectID uuid.UUID, courseID uint) (string, error) {
	image, err := db.GetTestImagesByProject(data, courseID)
	if err != nil {
		log.Printf("[ERROR] テスト画像の取得に失敗しました: %v", err)
		return "", err
	}
	job, err := db.GetModelPathsByTrainingJob(data, projectID)
	if err != nil {
		log.Printf("[ERROR] ジョブの取得に失敗しました: %v", err)
		return "", err
	}
	zipFilename := fmt.Sprintf("test_job_%d.zip", job.ID)
	zipPath := filepath.Join(os.TempDir(), zipFilename)

	zipFile, err := os.Create(zipPath)
	if err != nil {
		return "", fmt.Errorf("failed to create temp test zip file: %w", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()
	// ---------------------------------------------------------
	// テスト画像群をZIPに書き込む & JSON用のエントリを作成
	// ---------------------------------------------------------
	var resultList []model.TestResultEntry
	for _, img := range image {
		// ※ img.Path は実際のローカル環境のファイルパスを指していると仮定
		srcFile, err := os.Open(img.ImageURL)
		if err != nil {
			log.Printf("[WARN] テスト画像が開けません(スキップ): %s, err: %v", img.ImageURL, err)
			continue
		}

		// ZIP内での画像配置パス (例: images/test_0.jpg)
		ext := filepath.Ext(img.ImageURL)
		zipInnerFilename := fmt.Sprintf("images/test_%d%s", img.ID, ext)

		writer, err := zipWriter.Create(zipInnerFilename)
		if err != nil {
			srcFile.Close()
			return "", fmt.Errorf("failed to create zip entry for image %s: %w", zipInnerFilename, err)
		}

		if _, err := io.Copy(writer, srcFile); err != nil {
			srcFile.Close()
			return "", fmt.Errorf("failed to write image to zip: %w", err)
		}
		srcFile.Close()

		// 結果JSONの雛形を作成 (結果項目は空)
		resultList = append(resultList, model.TestResultEntry{
			ImageID:          img.ID,
			Filename:         zipInnerFilename,
			PredictedLabelID: nil, // 空欄
			Confidence:       0.0, // 空欄
		})
	}

	// ---------------------------------------------------------
	// AIモデル（WebModelRoot内の .keras ファイル）をZIPに組み込む
	// ---------------------------------------------------------
	if job.WebModelRoot != "" {
		// WebModelRoot（ディレクトリ）の中にあるファイルをスキャン
		files, err := os.ReadDir(job.WebModelRoot)
		if err != nil {
			log.Printf("[WARN] モデルディレクトリの読み込みに失敗しました: %s, err: %v", job.WebModelRoot, err)
		} else {
			modelCount := 0
			for _, file := range files {
				// ディレクトリではなく、かつ拡張子が ".keras" のファイルだけを狙い撃ち
				if !file.IsDir() && filepath.Ext(file.Name()) == ".keras" {
					srcPath := filepath.Join(job.WebModelRoot, file.Name())

					modelFile, err := os.Open(srcPath)
					if err != nil {
						log.Printf("[WARN] モデルファイルが開けません(スキップ): %s, err: %v", srcPath, err)
						continue
					}
					// ZIP内での配置パスを設定 (例: models/mobilenet_v3.keras)
					zipInnerModelPath := filepath.Join("models", file.Name())
					writer, err := zipWriter.Create(zipInnerModelPath)
					if err != nil {
						modelFile.Close()
						return "", fmt.Errorf("failed to create zip entry for model %s: %w", zipInnerModelPath, err)
					}
					// ストリームコピーでZIPに書き込み
					if _, err := io.Copy(writer, modelFile); err != nil {
						modelFile.Close()
						return "", fmt.Errorf("failed to write model file to zip: %w", err)
					}
					modelFile.Close()
					modelCount++
				}
			}
			log.Printf("[INFO] %d 個の .keras モデルファイルをZIPに同梱しました", modelCount)
		}
	} else {
		log.Printf("[WARN] WebModelRoot が空のため、モデルの同梱をスキップしました")
	}

	// ---------------------------------------------------------
	// 結果を記録するための雛形 JSON を作成して同梱
	// ---------------------------------------------------------
	itinerary := model.TestItinerary{
		StatusID: status, // 引数で受け取っているstatusIDをキャストしてセット
		Results:  resultList,
	}

	// itinerary を丸ごと JSON に変換する
	jsonBytes, err := json.MarshalIndent(itinerary, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal test results to json: %w", err)
	}

	jsonWriter, err := zipWriter.Create("test_itinerary.json")
	if err != nil {
		return "", fmt.Errorf("failed to create zip entry for test_itinerary.json: %w", err)
	}

	if _, err := jsonWriter.Write(jsonBytes); err != nil {
		return "", fmt.Errorf("failed to write test_itinerary.json to zip: %w", err)
	}

	// 明示的にクローズして確定
	if err := zipWriter.Close(); err != nil {
		return "", fmt.Errorf("failed to close zip writer: %w", err)
	}
	if err := zipFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close zip file: %w", err)
	}

	log.Printf("[INFO] テスト用ZIPファイルの作成が完了しました: %s", zipPath)
	return zipPath, nil
}
