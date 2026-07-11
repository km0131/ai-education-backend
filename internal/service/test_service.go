package service

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"ai-education/backend/internal/worker"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// テスト画像の登録
func CreatingTestDataset(data *gorm.DB, res model.ImageUploadResponse, file *multipart.FileHeader) error {
	// ファイル名生成
	filename := uuid.New().String() + filepath.Ext(file.Filename)
	savePath := fmt.Sprintf("images/test_photogrph/%d/%s", res.CourseID, filename)

	// ディレクトリ作成とファイル保存はここで実行
	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return err
	}

	//ファイルを物理的に作成して中身を書き込む
	// multipart.FileHeader からストリームを開く
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	// 保存先のファイルを新規作成
	dst, err := os.Create(savePath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dst.Close()

	// 中身をまるごとコピーしてディスクに書き出す
	if _, err = io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to save file to disk: %w", err)
	}

	batchID, err := uuid.Parse(res.BatchID)
	if err != nil {
		log.Printf("invalid batch_id: %q", res.BatchID)
		return err
	}

	flag, err := db.TestDataCheck(data, res.CourseID, batchID)

	if flag == false {
		log.Printf("[ERROR] すでに登録されているので登録出来ません。: %v", err)
		return err
	}

	err = db.CreatingTestDatasetDB(data, res.CourseID, savePath, batchID, res.CorrectLabelName)
	if err != nil {
		log.Printf("[ERROR] テスト画像の登録に失敗: %v", err)
		return err
	}

	return nil
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
