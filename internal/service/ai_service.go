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

func GenerateNewFilename(originalFilename string) string {
	// 元ファイルの拡張子を取得 (.jpg など)
	ext := filepath.Ext(originalFilename)

	// 新しいUUIDを生成して拡張子を結合
	return uuid.New().String() + ext
}

func SaveAndAnalyze(database *gorm.DB, userID uuid.UUID, rot model.ImageUploadRequest, file *multipart.FileHeader) (*model.AiPhotograph, error) {
	// ファイル名生成
	filename := uuid.New().String() + filepath.Ext(file.Filename)
	savePath := fmt.Sprintf("images/ai_photogrph/%s/%s", userID.String(), filename)

	// ディレクトリ作成とファイル保存はここで実行
	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return nil, err
	}

	//ファイルを物理的に作成して中身を書き込む
	// multipart.FileHeader からストリームを開く
	src, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer src.Close()

	// 保存先のファイルを新規作成
	dst, err := os.Create(savePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dst.Close()

	// 中身をまるごとコピーしてディスクに書き出す
	if _, err = io.Copy(dst, src); err != nil {
		return nil, fmt.Errorf("failed to save file to disk: %w", err)
	}

	var photo *model.AiPhotograph
	var targetConfigUUID uuid.UUID

	// トランザクションでデータの整合性を100%保証する
	err = database.Transaction(func(tx *gorm.DB) error {
		parsedSessionID, err := uuid.Parse(rot.UploadSessionID)
		// プロジェクト(箱)を作成
		config, err := db.GetOrCreateConfig(tx, userID, rot.CourseID, rot.Title, parsedSessionID)
		if err != nil {
			return err
		}

		targetConfigUUID = config.ProjectUUID

		// ラベル(カテゴリ)を最新版として作成
		category, err := db.CreateCategoryWithHistory(tx, config.ProjectUUID, int(rot.CategoryID), rot.CategoryTitle)
		if err != nil {
			return err
		}

		// 学習データを保存
		photo, err = db.CreatePhotograph(tx, category.CategoryID, userID, savePath)
		return err
	})

	if err != nil {
		log.Printf("[ERROR] SaveAndAnalyze トランザクション失敗: %v", err)
		return nil, err
	}

	// 画像評価キューに登録
	worker.AnalysisQueue <- photo.ID

	// AIカート作成

	log.Printf("[INFO] TrainingJob 作成を試みます。ConfigID: %s", targetConfigUUID)
	err = db.CreateTrainingJob(database, targetConfigUUID)
	if err != nil {
		log.Printf("[ERROR] CreateTrainingJob 失敗: %v", err)
		return nil, fmt.Errorf("failed to register training job: %w", err)
	}

	return photo, nil
}

func AICreation(database *gorm.DB, userId uuid.UUID, teacher bool, projectId uuid.UUID) (time.Time, error) {
	if teacher == false {
		author, err := db.AuthorCheck(database, userId, projectId)
		if err != nil {
			log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
			return time.Time{}, fmt.Errorf("AI作成の作成に失敗しました。: %w", err)
		}
		if !author {
			log.Printf("[ERROR] AI作成の作成権限が有りません。: %v", err)
			return time.Time{}, fmt.Errorf("AI作成の作成権限が有りません。: %w", err)
		}
	}
	// ステータスを確認して作成中ではないかチェック
	status, sttime, err := db.AIGenerationStatus(database, projectId)
	if err != nil {
		log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
		return time.Time{}, fmt.Errorf("AI作成の作成に失敗しました。: %w", err)
	}
	if !status {
		log.Printf("[WARN] すでにAIを作成中です。プロジェクトID: %s, 開始時間: %v", projectId, sttime)
		return sttime, fmt.Errorf("現在AIを作成中です（開始時間: %s）。しばらくお待ちください", sttime)
	}

	// ─── ここからAI作成用の処理 ───
	trainingJob, err := db.CreateTrainingJobWithSnapshot(database, projectId)
	if err != nil {
		log.Printf("[ERROR] 学習ジョブの作成に失敗: %v", err)
		return time.Time{}, fmt.Errorf("学習データのまとめ処理に失敗しました: %w", err)
	}
	// AI作成キューへJob IDを投入
	worker.TrainJobQueue <- trainingJob.ID

	log.Printf("[INFO] AI作成ジョブをキューに登録しました。JobID: %d", trainingJob.ID)
	return time.Time{}, nil
}
