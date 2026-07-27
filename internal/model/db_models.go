package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type FloatSlice []float64

// Scan implements sql.Scanner so FloatSlice can be read back from a jsonb column.
func (f *FloatSlice) Scan(value any) error {
	if value == nil {
		*f = nil
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("unsupported Scan, storing driver.Value type %T into type *model.FloatSlice", value)
	}

	if len(bytes) == 0 {
		*f = nil
		return nil
	}

	return json.Unmarshal(bytes, f)
}

// Value implements driver.Valuer so FloatSlice is persisted as jsonb.
func (f FloatSlice) Value() (driver.Value, error) {
	if f == nil {
		return nil, nil
	}
	return json.Marshal(f)
}

// RegistrationTicket は仮登録時に発行するチケットの永続化モデルです。
type RegistrationTicket struct {
	ID               string    `gorm:"primaryKey" json:"id"`
	ExhibitedNumbers string    `gorm:"type:text;not null" json:"exhibited_numbers"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `gorm:"index" json:"expires_at"`
}

// User はユーザー情報の永続化モデルです。
type User struct {
	ID            uuid.UUID `gorm:"type:uuid;default:gen_random_uuid();primaryKey"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     gorm.DeletedAt `gorm:"index"`
	Name          string         `gorm:"type:text;unique;not null"`
	Password      string         `gorm:"type:varchar(255);not null"`
	PasswordGroup string         `gorm:"type:text;not null"`
	Email         string         `gorm:"type:text"`
	Teacher       bool           `gorm:"type:boolean;not null"`
	QRpassword    string         `gorm:"type:varchar(255);not null"`
}

// Certification は画像認証に使う画像マスタの永続化モデルです。
type Certification struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"not null"`
}

// Course はクラス情報の永続化モデルです。
type Course struct {
	gorm.Model
	Title       string `gorm:"not null"`
	Description string
	InviteCode  string    `gorm:"unique;not null;index"`
	TeacherID   uuid.UUID `gorm:"type:uuid;not null;index"`
	Teacher     User      `gorm:"foreignKey:TeacherID;references:ID"`
	ThemeColor  string
}

// 生徒とクラスを結ぶリレーションテーブル
type CourseEnrollment struct {
	gorm.Model
	// クラスへの外部キー
	CourseID uint   `gorm:"not null;index:idx_course_user,unique;comment:クラスID"`
	Course   Course `gorm:"foreignKey:CourseID;constraint:OnDelete:CASCADE"`

	// ユーザーへの外部キー
	UserID uuid.UUID `gorm:"type:uuid;not null;index:idx_course_user,unique;comment:ユーザーID"`
	User   User      `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`

	// 拡張用フィールド（必要に応じて）
	Role       string    `gorm:"type:varchar(20);default:'student';comment:クラス内ロール(student/co-teacher)"`
	EnrolledAt time.Time `gorm:"autoCreateTime;comment:参加日時"`
}

// Enrollment はクラス履修関係の永続化モデルです。
type Enrollment struct {
	gorm.Model
	CourseID  uint      `gorm:"not null;index"`
	Course    Course    `gorm:"foreignKey:CourseID"`
	StudentID uuid.UUID `gorm:"type:uuid;not null;index"`
}

// システムのログを保存するテーブル
type SystemLog struct {
	ID        uint      `gorm:"primaryKey"`
	Level     string    `gorm:"type:varchar(10);index"` // エラーレベル（例: INFO, ERROR）
	UserID    *uint     `gorm:"index"`                  // 関連するユーザーID（あれば）
	Action    string    `gorm:"type:varchar(50)"`       // 実行されたアクションの種類（例: "login_attempt", "registration"）
	Message   string    `gorm:"type:text"`              // ログの詳細メッセージ
	Detail    string    `gorm:"type:text"`              // 元のエラーメッセージ
	Timestamp time.Time `gorm:"autoCreateTime"`         // ログのタイムスタンプ
}

// AiConfiguration: AIプロジェクトの「箱」
type AiConfiguration struct {
	gorm.Model // id (bigint) は自動生成される
	// 🌟 referencesの対象にするため uniqueIndex を明示
	ProjectUUID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null"`
	StudentID   uuid.UUID `gorm:"type:uuid;not null;index"`
	CourseID    uint      `gorm:"not null;index"`
	Title       string    `gorm:"size:255"`
	IsShared    bool      `gorm:"default:false"`

	// リレーション：参照先（references）にProjectUUIDを明示
	Categories   []AiCategory    `gorm:"foreignKey:ConfigID;references:ProjectUUID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	TrainingJobs []AiTrainingJob `gorm:"foreignKey:ConfigID;references:ProjectUUID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

// AiCategory: ラベル情報
type AiCategory struct {
	gorm.Model              // id (bigint) は自動生成される
	ConfigID      uuid.UUID `gorm:"type:uuid;not null;index"`
	CategoryID    uuid.UUID `gorm:"type:uuid;uniqueIndex;not null"`
	CategoryIndex int       `gorm:"not null"`
	Title         string    `gorm:"size:255"`
	Explanation   string    `gorm:"type:text"`

	// リレーション：参照先（references）にCategoryIDを明示
	Photographs []AiPhotograph `gorm:"foreignKey:CategoryID;references:CategoryID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

// ConversionStatus: フロント(createImageBitmap/heic2any)のどちらでも変換できなかった
// HEIC/RAWファイルを、バックエンドがheif-convert/exiftoolで非同期に変換する際の進行状況。
// Ready: リサイズ済み画像がそのまま使用可能(通常はこれ) / Processing: バックグラウンド変換中
// / Failed: バックグラウンド変換が失敗(ConversionErrorに理由が入る)
const (
	ConversionStatusReady      = "ready"
	ConversionStatusProcessing = "processing"
	ConversionStatusFailed     = "failed"
)

// AiPhotograph: 学習データの最小単位
type AiPhotograph struct {
	gorm.Model // id (bigint) は自動生成される
	// 親の参照先に合わせて明確に type:uuid を指定
	CategoryID     uuid.UUID `gorm:"type:uuid;not null;index"`
	StudentID      uuid.UUID `gorm:"type:uuid;not null;index"`
	PhotographPath string    `gorm:"not null"`
	IsAnalyzed     bool      `gorm:"default:false"`

	// HEIC/RAWフォールバック変換(heif-convert/exiftool)の進行状況。既定はReady(同期処理で完結)。
	ConversionStatus string `gorm:"type:varchar(20);not null;default:'ready';index"`
	ConversionError  string `gorm:"type:text"`

	Saturation      float64    `gorm:"type:float"`
	Brightness      float64    `gorm:"type:float"`
	Sharpness       float64    `gorm:"type:float"`
	DiversityVector FloatSlice `gorm:"type:jsonb"`
}

// AiTrainingJob: 学習の「バージョン」を管理
type AiTrainingJob struct {
	gorm.Model
	ConfigID  uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_config_version"`
	Version   int       `gorm:"not null;uniqueIndex:idx_config_version"`
	Status    string    `gorm:"type:varchar(20);index"`
	IsCurrent bool      `gorm:"not null;default:false;index"`
	// AIの統計・評価データ
	AvgSaturation  float64 `gorm:"type:float"`
	DiversityScore float64 `gorm:"type:float"`
	// 修正：3モデル分の最終精度（Accuracy/Loss）をJSONでまとめて入れる
	// 例: {"mobilenet_v3": {"accuracy": 0.92, "loss": 0.15}, ...}
	AccuracySummary string `gorm:"type:text"`
	// 3モデル分の学習履歴（エポックごとの推移）JSON
	LearningCurve string `gorm:"type:text"`
	// ファイルパス関連
	ModelZipPath string `gorm:"type:varchar(255)"` // .keras本体が含まれるオリジナルモデルの保存先
	WebModelRoot string `gorm:"type:varchar(255)"` // フロント(JS)が読み込む解凍先ディレクトリのパス
}

// AiTrainingJobSnapshot: どのJobにどの写真が含まれていたかの中間テーブル
type AiTrainingJobSnapshot struct {
	gorm.Model
	AiTrainingJobID uint `gorm:"not null;index"` // どの学習バージョンか
	PhotographID    uint `gorm:"not null"`       // どの写真か
	LabelID         int  `gorm:"not null"`       // その時点でのラベル番号
}

type TestImage struct {
	gorm.Model
	CourseID         uint      `gorm:"not null;index;"`                   // どのクラス（プロジェクト）のデータセットか
	BatchID          uuid.UUID `gorm:"not null;type:varchar(191);index;"` //編集や送信時の識別
	ImageURL         string    `gorm:"not null;type:text"`                // 画像の配信URL/パス
	CorrectLabelName string    `gorm:"not null;type:varchar(50);index"`   // 先生が追加・選択したラベル名

	// HEIC/RAWフォールバック変換(heif-convert/exiftool)の進行状況。既定はReady(同期処理で完結)。
	ConversionStatus string `gorm:"type:varchar(20);not null;default:'ready';index"`
	ConversionError  string `gorm:"type:text"`
}

// StudentTestMapping: 先生のラベルと生徒のラベルの対応表（中間テーブル）
type StudentTestMapping struct {
	gorm.Model
	ProjectUUID      uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_project_student"` // どの生徒のプロジェクトか
	CourseID         uint      `gorm:"not null;index;"`                                    // クラスID
	TeacherLabelName string    `gorm:"not null"`                                           // 先生側のラベル名（本殿）
	StudentLabelName string    `gorm:"not null;uniqueIndex:idx_project_student"`           // 生徒側のラベル名（本）
}

// StudentTestJob: テスト実行全体のステータスやサマリーを管理する親テーブル
type StudentTestJob struct {
	gorm.Model
	UserID        uuid.UUID `gorm:"type:uuid;not null;index"`
	TrainingJobID uint      `gorm:"not null;index"` // どのAIモデル（学習バージョン）を使ったか
	//  "pending" (準備中), "running" (Pythonで推論中), "success" (完了), "failed" (エラー)
	Status string `gorm:"type:varchar(20);not null;default:'pending';index"`
	// 全体での正解率（例: 0.85 ➔ 85%正解）。子テーブルを集計してここに表示
	TotalAccuracy float64 `gorm:"type:float;not null;default:0.0"`
	//　実行時エラーの理由などを残せるように
	ErrorMessage string `gorm:"type:text"`
	// 1対多のリレーション定義
	Models []StudentTestJobModel `gorm:"foreignKey:StudentTestJobID"`
}

// StudentTestJobModel: 1テストジョブ内で「どのモデルを回したか」＋そのモデルの集計結果
// グラフ表示（精度推移・モデル間比較）はこのテーブルだけで完結させる想定
type StudentTestJobModel struct {
	gorm.Model
	StudentTestJobID uint    `gorm:"not null;index"`      // どのテスト実行セッション（Job）に属するか
	ModelName        string  `gorm:"not null;index"`      // モデル名（例: "mobilenet_v3", "resnet50"）※1ジョブに複数モデルがある場合の識別キー
	Accuracy         float64 `gorm:"not null;type:float"` // このモデルの正解率（グラフ用の集計値）
	Loss             float64 `gorm:"not null;type:float"` // このモデルのロス値（グラフ用の集計値）
	TotalImages      int     `gorm:"not null"`            // このモデルでテストした画像枚数
}

// StudentTestResultSnapshot: 画像単位の生データ（混同行列や間違えた画像の一覧など、詳細分析用）
// 普段のグラフ表示では触らず、詳細を見たいときだけこちらを参照する
type StudentTestResultSnapshot struct {
	gorm.Model
	StudentTestJobModelID uint    `gorm:"not null;index"`      // どのモデルの実行結果か（StudentTestJobModel.ID を参照）※StudentTestJobIDではなくこちらを参照する点に注意
	TestImageID           uint    `gorm:"not null"`            // どのテスト画像か（TestImage.ID）
	PredictedLabelID      int     `gorm:"not null"`            // 生徒のAIが出した予測ラベルID（例: 3）
	Confidence            float64 `gorm:"not null;type:float"` // 確信度（例: 0.92）
	IsCorrect             bool    `gorm:"not null"`            // マッピングを基準にした正誤（true/false）
}
