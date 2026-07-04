package controller

import (
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/model"
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// HandleModelReady は GCP からのモデル完了通知を受け取る
func HandleModelReady(c *gin.Context) {
	database := db.DB

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
	zipStorageDir := "./storage/zips"
	_ = os.MkdirAll(targetDir, os.ModePerm)
	_ = os.MkdirAll(zipStorageDir, os.ModePerm)

	savedZipPath := filepath.Join(zipStorageDir, fmt.Sprintf("%d.zip", input.JobID))
	if err := c.SaveUploadedFile(fileHeader, savedZipPath); err != nil {
		log.Printf("[ERROR] ZIPの保存失敗: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ファイルの保存に失敗しました"})
		return
	}

	// ZIPファイルを targetDir に解凍 (3つのモデルのフォルダが展開される)
	if err := unzip(savedZipPath, targetDir); err != nil {
		log.Printf("[ERROR] ZIPの解凍失敗: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "モデルデータの展開に失敗しました"})
		return
	}

	// DB上の該当ジョブを検索
	var job model.AiTrainingJob
	if err := database.First(&job, input.JobID).Error; err != nil {
		log.Printf("[ERROR] 指定されたJobIDが見つかりません: %d", input.JobID)
		c.JSON(http.StatusNotFound, gin.H{"error": "該当する学習ジョブが存在しません"})
		return
	}

	// 値の更新データをマップで作成
	// ※ 構造体だと 0.0 の値が省略されてしまうため、map[string]interface{} を使います
	updates := map[string]interface{}{
		"status":           "production", // ステータスを「本番利用可能」に
		"avg_saturation":   input.AvgSaturation,
		"diversity_score":  input.DiversityScore,
		"accuracy_summary": input.Accuracy,
		"learning_curve":   input.LearningCurve, // 3モデル分の履歴が入ったJSON文字列
		"model_zip_path":   savedZipPath,        // 将来のテスト用に、確定したZIPパスを記録
		"web_model_root":   targetDir,           // 新設カラム（解凍先ルート）
	}

	// 8. DBをアップデート
	if err := database.Model(&job).Updates(updates).Error; err != nil {
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
