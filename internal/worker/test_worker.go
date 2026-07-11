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
	"strings"

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
	// テスト画像群をZIPに書き込む & 結果エントリの雛形を作成（全モデル共通）
	// ---------------------------------------------------------
	var baseResultList []model.TestResultEntry
	for _, img := range image {
		srcFile, err := os.Open(img.ImageURL)
		if err != nil {
			log.Printf("[WARN] テスト画像が開けません(スキップ): %s, err: %v", img.ImageURL, err)
			continue
		}

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

		// 先生の正解ラベル(img.CorrectLabelName)から生徒ラベルID(訓練時のCategoryIndex)へ変換
		trueLabelID, err := db.ResolveStudentLabelFromTeacherLabel(data, projectID, img.CorrectLabelName)
		if err != nil {
			log.Printf("[WARN] ラベルマッピングに失敗(スキップ): image_id=%d, err: %v", img.ID, err)
			continue
		}

		baseResultList = append(baseResultList, model.TestResultEntry{
			ImageID:          img.ID,
			Filename:         zipInnerFilename,
			TrueLabelID:      trueLabelID,
			PredictedLabelID: nil,
			Confidence:       0.0,
		})
	}

	// ---------------------------------------------------------
	// AIモデル（.tfliteファイル）をZIPに組み込み、モデル名一覧を収集
	// 3モデルとも.tfliteへ移行済み(Pythonの/testエンドポイントがai_edge_litert.Interpreterで評価する)。
	// .keras/.pt（アーカイブ用の変換前モデル）は評価対象に含めない。
	// ---------------------------------------------------------
	const evalModelExt = ".tflite"
	var modelNames []string
	if job.WebModelRoot != "" {
		files, err := os.ReadDir(job.WebModelRoot)
		if err != nil {
			log.Printf("[WARN] モデルディレクトリの読み込みに失敗しました: %s, err: %v", job.WebModelRoot, err)
		} else {
			for _, file := range files {
				if !file.IsDir() && filepath.Ext(file.Name()) == evalModelExt {
					srcPath := filepath.Join(job.WebModelRoot, file.Name())
					modelFile, err := os.Open(srcPath)
					if err != nil {
						log.Printf("[WARN] モデルファイルが開けません(スキップ): %s, err: %v", srcPath, err)
						continue
					}
					zipInnerModelPath := filepath.Join("models", file.Name())
					writer, err := zipWriter.Create(zipInnerModelPath)
					if err != nil {
						modelFile.Close()
						return "", fmt.Errorf("failed to create zip entry for model %s: %w", zipInnerModelPath, err)
					}
					if _, err := io.Copy(writer, modelFile); err != nil {
						modelFile.Close()
						return "", fmt.Errorf("failed to write model file to zip: %w", err)
					}
					modelFile.Close()

					modelName := strings.TrimSuffix(file.Name(), evalModelExt) // ← モデル名をファイル名から抽出
					modelNames = append(modelNames, modelName)
				}
			}
			log.Printf("[INFO] %d 個の.tfliteモデルファイルをZIPに同梱しました", len(modelNames))
		}
	} else {
		log.Printf("[WARN] WebModelRoot が空のため、モデルの同梱をスキップしました")
	}
	// ラベル
	labelMapSrc := filepath.Join(job.WebModelRoot, "label_map.json")
	if _, err := os.Stat(labelMapSrc); err == nil {
		labelMapFile, err := os.Open(labelMapSrc)
		if err == nil {
			writer, err := zipWriter.Create("models/label_map.json")
			if err == nil {
				io.Copy(writer, labelMapFile)
			}
			labelMapFile.Close()
		}
	} else {
		log.Printf("[WARN] label_map.json が見つかりません: %s", labelMapSrc)
	}

	// ---------------------------------------------------------
	// itinerary: モデル名ごとに同じ画像リストを割り当てる
	// ---------------------------------------------------------
	modelsMap := make(map[string][]model.TestResultEntry, len(modelNames))
	for _, name := range modelNames {
		modelsMap[name] = baseResultList // 読み取り専用として使うので共有スライスでOK
	}

	itinerary := model.TestItinerary{
		StudentTestJobID: job.ID, // ← 実際のStudentTestJobのIDに置き換えてください
		Models:           modelsMap,
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
