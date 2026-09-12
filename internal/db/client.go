package db

import (
	"fmt"
	"os"
	"strings"

	"ai-education/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
		&model.FeatureType{}, // Courseより先に(FeatureTypeIDのFK参照先のため)
		&model.Course{},
		&model.CourseEnrollment{},
		&model.Enrollment{},
		// AIプロジェクト設計基盤
		&model.AiConfiguration{}, // 親
		&model.AiCategory{},      // 子(ConfigID)
		&model.AiPhotograph{},    // 孫(CategoryID)
		// AI学習と推論
		&model.AiTrainingJob{}, // 紐付き(ConfigID)
		&model.AiTrainingJobSnapshot{},
		// AIテスト用
		&model.TestImage{},
		&model.StudentTestMapping{},
		&model.StudentTestResultSnapshot{},
		&model.StudentTestJob{},
		&model.StudentTestJobModel{},
		&model.StudentTestResultSnapshot{},
		// 中高生向けプログラム学習機能
		&model.ProgramSandbox{},
		&model.ContentReport{},
	}

	// まとめて実行
	err := DB.AutoMigrate(models...)

	if err != nil {
		fmt.Printf("[ERROR] マイグレーション中にエラーが発生しました: %v\n", err)
		return err
	}

	// feature_types のシード(存在しなければ挿入。冪等なので毎回の起動時に実行して問題ない)
	seedFeatureTypes := []model.FeatureType{
		{Key: model.FeatureTypeKeyImageClassification, Name: "画像分類AI"},
		{Key: model.FeatureTypeKeyWebDev, Name: "Web開発環境"},
	}
	for _, ft := range seedFeatureTypes {
		if err := DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&ft).Error; err != nil {
			fmt.Printf("[ERROR] feature_typesのシードに失敗しました(key=%s): %v\n", ft.Key, err)
			return err
		}
	}

	// courses.feature_type_id の安全な3段階移行:
	// 1. AutoMigrateで既にNULL許可のカラム(+FK制約)が追加済み(上のmodels一覧、既存行はNULLのままでもFK制約には抵触しない)
	// 2. 既存の全クラスに「画像分類AI」を一括設定
	if err := DB.Exec(`
		UPDATE courses
		SET feature_type_id = (SELECT id FROM feature_types WHERE key = ?)
		WHERE feature_type_id IS NULL
	`, model.FeatureTypeKeyImageClassification).Error; err != nil {
		fmt.Printf("[ERROR] courses.feature_type_idの一括更新に失敗しました: %v\n", err)
		return err
	}
	// 3. 全行埋まったことを確認してからNOT NULL化(Postgresでは既にNOT NULLの列に
	//    再度SET NOT NULLしてもエラーにならないため、この関数を毎起動時に呼んでも安全)
	var remainingNull int64
	if err := DB.Raw(`SELECT COUNT(*) FROM courses WHERE feature_type_id IS NULL`).Scan(&remainingNull).Error; err != nil {
		fmt.Printf("[ERROR] feature_type_idの未設定件数確認に失敗しました: %v\n", err)
		return err
	}
	if remainingNull == 0 {
		if err := DB.Exec(`ALTER TABLE courses ALTER COLUMN feature_type_id SET NOT NULL`).Error; err != nil {
			fmt.Printf("[ERROR] courses.feature_type_idのNOT NULL化に失敗しました: %v\n", err)
			return err
		}
	} else {
		fmt.Printf("[WARN] courses.feature_type_id が %d 件未設定のため、NOT NULL化をスキップしました\n", remainingNull)
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
