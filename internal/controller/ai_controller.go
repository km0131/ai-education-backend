package controller

import (
	"ai-education/backend/internal/model"
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// HandleModelReady は GCP からのモデル完了通知を受け取る
func HandleModelReady(c *gin.Context) {
	// データベース接続の取得（環境に合わせて調整してください）
	db := c.MustGet("db").(*gorm.DB)

	// フォームテキストデータのバインド (c.ShouldBind で multipart も対応)
	var input model.ModelReadyInput
	if err := c.ShouldBind(&input); err != nil {
		log.Printf("[ERROR] コールバックのバリデーション失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "無効なリクエストパラメータです"})
		return
	}

	// 送られてきたモデルZIPファイルの取得
	fileHeader, err := c.FormFile("model_zip")
	if err != nil {
		log.Printf("[ERROR] モデルZIPの取得失敗: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "model_zip ファイルが見つかりません"})
		return
	}

	// ZIPファイルを一時保存してから解凍するディレクトリを決定
	// 将来フロント（WebGPU / Transformers.js）がロードしやすい配置にします
	targetDir := fmt.Sprintf("./storage/models/%d", input.JobID)
	_ = os.MkdirAll(targetDir, os.ModePerm)

	tempZipPath := filepath.Join(os.TempDir(), fileHeader.Filename)
	if err := c.SaveUploadedFile(fileHeader, tempZipPath); err != nil {
		log.Printf("[ERROR] ZIPの一時保存失敗: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ファイルの保存に失敗しました"})
		return
	}
	defer os.Remove(tempZipPath) // 処理が終わったら一時ZIPは消去

	// ZIPファイルを targetDir に解凍 (3つのモデルのフォルダが展開される)
	if err := unzip(tempZipPath, targetDir); err != nil {
		log.Printf("[ERROR] ZIPの解凍失敗: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "モデルデータの展開に失敗しました"})
		return
	}

	// DB上の該当ジョブを検索
	var job model.AiTrainingJob
	if err := db.First(&job, input.JobID).Error; err != nil {
		log.Printf("[ERROR] 指定されたJobIDが見つかりません: %d", input.JobID)
		c.JSON(http.StatusNotFound, gin.H{"error": "該当する学習ジョブが存在しません"})
		return
	}

	// 値の更新データをマップで作成
	// ※ 構造体だと 0.0 の値が省略されてしまうため、map[string]interface{} を使います
	updates := map[string]interface{}{
		"status":          "production", // ステータスを「本番利用可能」に
		"avg_saturation":  input.AvgSaturation,
		"diversity_score": input.DiversityScore,
		"accuracy":        input.Accuracy,
		"loss":            input.Loss,
		"learning_curve":  input.LearningCurve, // 3モデル分の履歴が入ったJSON文字列
		"model_zip_path":  targetDir,
	}

	// 8. DBをアップデート
	if err := db.Model(&job).Updates(updates).Error; err != nil {
		log.Printf("[ERROR] DBの更新失敗: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "データベースの更新に失敗しました"})
		return
	}

	log.Printf("[INFO] JobID: %d のモデル(3種)と学習履歴を正常に保存しました", input.JobID)
	c.JSON(http.StatusOK, gin.H{"status": "success", "job_id": input.JobID})
}

// unzip は指定されたZIPファイルを解凍するヘルパー関数です
func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}
