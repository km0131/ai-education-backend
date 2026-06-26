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

// AiPhotograph: 学習データの最小単位
type AiPhotograph struct {
	gorm.Model // id (bigint) は自動生成される
	// 🌟 親の参照先に合わせて明確に type:uuid を指定
	CategoryID     uuid.UUID `gorm:"type:uuid;not null;index"`
	StudentID      uuid.UUID `gorm:"type:uuid;not null;index"`
	PhotographPath string    `gorm:"not null"`
	IsAnalyzed     bool      `gorm:"default:false"`

	Saturation      float64    `gorm:"type:float"`
	Brightness      float64    `gorm:"type:float"`
	Sharpness       float64    `gorm:"type:float"`
	DiversityVector FloatSlice `gorm:"type:jsonb"`
}

// AiTrainingJob: 学習の「バージョン」を管理
type AiTrainingJob struct {
	gorm.Model               // id (bigint) は自動生成される
	ConfigID       uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_config_status"`
	Status         string    `gorm:"type:varchar(20);uniqueIndex:idx_config_status"`
	ModelPath      string    `gorm:"size:255"`
	AvgSaturation  float64   `gorm:"type:float"`
	DiversityScore float64   `gorm:"type:float"`
	Accuracy       float64   `gorm:"type:float"`
	LearningCurve  string    `gorm:"type:text"`         // 3モデル分の学習履歴JSONが入る
	ModelZipPath   string    `gorm:"type:varchar(255)"` // 解凍・配置したモデルのパスなど
}

// AiTrainingJobSnapshot: どのJobにどの写真が含まれていたかの中間テーブル
type AiTrainingJobSnapshot struct {
	gorm.Model
	AiTrainingJobID uint `gorm:"not null;index"` // どの学習バージョンか
	PhotographID    uint `gorm:"not null"`       // どの写真か
	LabelID         int  `gorm:"not null"`       // その時点でのラベル番号
}
