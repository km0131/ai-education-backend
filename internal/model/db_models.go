package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type FloatSlice []float64

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
// 作成者とコースが紐付き、複数回学習（再学習）のベースとなる
type AiConfiguration struct {
	gorm.Model
	ProjectUUID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null"` // 外部参照・共有用UUID
	StudentID   uuid.UUID `gorm:"type:uuid;not null;index"`       // プロジェクト作成者
	CourseID    uint      `gorm:"not null;index"`                 // 所属クラス
	Title       string    `gorm:"size:255"`                       // プロジェクト名
	IsShared    bool      `gorm:"default:false"`                  // クラスメンバーと共有するかどうか
	// リレーション
	Categories   []AiCategory    `gorm:"foreignKey:ConfigID"` // ラベル
	TrainingJobs []AiTrainingJob `gorm:"foreignKey:ConfigID"` // 学習履歴
}

// AiCategory: ラベル情報
// 「作り直し」に対応するため、ConfigIDとIndexで最新を追跡する
type AiCategory struct {
	gorm.Model
	ConfigID      uuid.UUID      `gorm:"not null;index"`
	CategoryID    uuid.UUID      `gorm:"not null;index"`
	CategoryIndex int            `gorm:"not null"`
	Title         string         `gorm:"size:255"`  // ラベル名
	Explanation   string         `gorm:"type:text"` // 説明文
	Photographs   []AiPhotograph `gorm:"foreignKey:CategoryID"`
}

// AiPhotograph: 学習データの最小単位
type AiPhotograph struct {
	gorm.Model
	CategoryID     uuid.UUID `gorm:"not null;index"`
	StudentID      uuid.UUID `gorm:"type:uuid;not null;index"`
	PhotographPath string    `gorm:"not null"`
	IsAnalyzed     bool      `gorm:"default:false"`
	// --- 画像1枚ごとの統計情報 ---
	Saturation      float64    `gorm:"type:float"` // 彩度
	Brightness      float64    `gorm:"type:float"` // 明度
	Sharpness       float64    `gorm:"type:float"` // 追加
	DiversityVector FloatSlice `gorm:"type:jsonb"` // 追加: 配列を格納
}

// AiTrainingJob: 学習の「バージョン」を管理
// ユーザーが「再学習」を押すたびに、このレコードが一つ増える
type AiTrainingJob struct {
	gorm.Model
	ConfigID  uuid.UUID `gorm:"not null;index"`
	Status    string    `gorm:"type:varchar(20)"` // pending, completed等
	ModelPath string    `gorm:"size:255"`         // 作成されたモデルファイルへのパス
	// --- 学習実行時のデータセット統計 ---
	AvgSaturation  float64 `gorm:"type:float"` // 使用した画像全体の平均彩度
	DiversityScore float64 `gorm:"type:float"` // この学習データセットの多様性スコア
	Accuracy       float64 `gorm:"type:float"` // 学習結果の精度
}
