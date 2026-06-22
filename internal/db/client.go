package db

import (
	"fmt"
	"os"
	"strings"

	"ai-education/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func InitDB() {
	hosts := buildDBHostCandidates(os.Getenv("DB_HOST"))
	var lastErr error
	for _, host := range hosts {
		dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=disable",
			host,
			os.Getenv("DB_USER"),
			os.Getenv("DB_PASSWORD"),
			os.Getenv("DB_NAME"),
			os.Getenv("DB_PORT"),
		)

		DB, lastErr = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if lastErr == nil {
			fmt.Printf("データベースに接続しました (host=%s)\n", host)
			return
		}
	}

	panic("データベースへの接続に失敗しました: " + lastErr.Error())
}

func buildDBHostCandidates(configuredHost string) []string {
	var hosts []string
	add := func(host string) {
		host = strings.TrimSpace(host)
		if host == "" {
			return
		}
		for _, existing := range hosts {
			if existing == host {
				return
			}
		}
		hosts = append(hosts, host)
	}

	add(configuredHost)
	add("db")

	return hosts
}

// または package db の init 時に全モデルをマイグレート
func Migrate() error {
	fmt.Println("--- データベースマイグレーションを開始します ---")

	// マイグレーション対象のモデルリスト
	models := []interface{}{
		&model.User{},
		&model.Certification{},
		&model.RegistrationTicket{},
		&model.SystemLog{},
		// クラス関連
		&model.Course{},
		&model.CourseEnrollment{},
		&model.Enrollment{},
		// AIプロジェクト設計基盤
		&model.AiConfiguration{}, // 親
		&model.AiCategory{},      // 子(ConfigID)
		&model.AiPhotograph{},    // 孫(CategoryID)
		// AI学習と推論
		&model.AiModel{},       // 紐付き(ConfigID)
		&model.AiTrainingJob{}, // 紐付き(ConfigID)
	}

	// まとめて実行
	err := DB.AutoMigrate(models...)

	if err != nil {
		fmt.Printf("[ERROR] マイグレーション中にエラーが発生しました: %v\n", err)
		return err
	}

	// テーブルが実際に作成されたか確認するためのログ（デバッグ用）
	for _, m := range models {
		if DB.Migrator().HasTable(m) {
			fmt.Printf("[INFO] テーブル確認済み: %T\n", m)
		} else {
			fmt.Printf("[WARN] テーブルが存在しません: %T\n", m)
		}
	}

	fmt.Println("--- 全てのマイグレーションが正常に完了しました ---")
	return nil
}
