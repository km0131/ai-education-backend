package service

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/worker"
	"fmt"
	"log"
	"mime/multipart"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// テスト画像の登録
func CreatingTestDataset(data *gorm.DB, res model.ImageUploadResponse, file *multipart.FileHeader, resizedFile *multipart.FileHeader) (*model.TestImage, error) {
	// ファイル名生成(リサイズ版とオリジナル版で同じUUIDを共有し、拡張子だけ変える)
	baseName := uuid.New().String()
	originalFilename := baseName + filepath.Ext(file.Filename)
	resizedFilename := baseName + ".jpg"

	// リサイズ版は今までと全く同じパス。オリジナルはディレクトリ名だけ差し替えたパスに保存する
	savePath := fmt.Sprintf("images/test_photogrph/%d/%s", res.CourseID, resizedFilename)
	originalSavePath := fmt.Sprintf("images/test_photogrph_original/%d/%s", res.CourseID, originalFilename)

	outcome, err := saveOriginalAndResizedImage(file, resizedFile, originalSavePath, savePath)
	if err != nil {
		return nil, err
	}

	batchID, err := uuid.Parse(res.BatchID)
	if err != nil {
		log.Printf("invalid batch_id: %q", res.BatchID)
		return nil, err
	}

	flag, err := db.TestDataCheck(data, res.CourseID, batchID)

	if flag == false {
		log.Printf("[ERROR] すでに登録されているので登録出来ません。: %v", err)
		return nil, err
	}

	conversionStatus := model.ConversionStatusReady
	if outcome == ResizeOutcomeNeedsFallback {
		conversionStatus = model.ConversionStatusProcessing
	}

	testImage, err := db.CreatingTestDatasetDB(data, res.CourseID, savePath, batchID, res.CorrectLabelName, conversionStatus)
	if err != nil {
		log.Printf("[ERROR] テスト画像の登録に失敗: %v", err)
		return nil, err
	}

	if outcome == ResizeOutcomeNeedsFallback {
		// heif-convert/exiftoolでの変換をHTTPリクエストの外側(バックグラウンド)で実行する
		testImageID := testImage.ID
		scheduleFallbackConversion(fallbackJob{
			Database:         data,
			OriginalPath:     originalSavePath,
			OriginalFilename: file.Filename,
			SavePath:         savePath,
			UpdateStatus: func(tx *gorm.DB, status, errMsg string) error {
				return db.UpdateTestImageConversionStatus(tx, testImageID, status, errMsg)
			},
		})
	}

	return testImage, nil
}

func TestExecutionService(data *gorm.DB, projectId uuid.UUID, courseID uint, isTeacher bool, userId uuid.UUID) (time.Time, error) {
	if isTeacher == false {
		author, err := db.AuthorCheck(data, userId, projectId)
		if err != nil {
			log.Printf("[ERROR] AIテストに失敗しました。: %v", err)
			return time.Time{}, err
		}
		if !author {
			log.Printf("[ERROR] ラベル修正の権限が有りません。: %v", err)
			return time.Time{}, fmt.Errorf("ラベル修正の権限が有りません")
		}
	}
	status, upTime, err := db.UpTestStatus(data, projectId, userId)
	if err != nil {
		log.Printf("[ERROR] テストステータスの作成に失敗しました。: %v", err)
		return time.Time{}, fmt.Errorf("テストステータスの作成に失敗しました")
	}
	if upTime != nil {
		log.Printf("[ERROR] 現在テスト中です。: %v", upTime)
		return status.UpdatedAt, fmt.Errorf("現在テスト中です")
	}

	// ZIP作成とPythonへの送信は非同期（GPUスケジューラ経由）で行う
	worker.Scheduler.Enqueue(&worker.GPUJobRequest{
		Kind:      worker.JobKindTest,
		Priority:  worker.PriorityTest,
		StatusID:  status.ID,
		ProjectID: projectId,
		CourseID:  courseID,
	})
	err = db.TestStatusDB(data, status.ID)
	if err != nil {
		log.Printf("[ERROR] ステータスをテスト中に変更。: %v", err)
		return time.Time{}, fmt.Errorf("ステータスをテスト中に変更")
	}

	return time.Time{}, nil
}
