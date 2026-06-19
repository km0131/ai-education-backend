package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

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

// Enrollment はクラス履修関係の永続化モデルです。
type Enrollment struct {
	gorm.Model
	CourseID  uint      `gorm:"not null;index"`
	Course    Course    `gorm:"foreignKey:CourseID"`
	StudentID uuid.UUID `gorm:"type:uuid;not null;index"`
}

// AiExplanation はAI説明セットの親テーブルです。
type AiExplanation struct {
	gorm.Model
	StudentID   uuid.UUID      `gorm:"type:uuid;not null;index"`
	CourseID    uint           `gorm:"not null;index"`
	Name        string         `gorm:"size:255"`
	Explanation string         `gorm:"type:text"`
	Photographs []AiPhotograph `gorm:"foreignKey:AiExplanationID"`
}

// AiPhotograph はAI説明セットの子テーブルです。
type AiPhotograph struct {
	gorm.Model
	AiExplanationID uint   `gorm:"not null;index"`
	PhotographPath  string `gorm:"not null"`
}

// AiModel は学習済みモデル情報の永続化モデルです。
type AiModel struct {
	gorm.Model
	StudentID uuid.UUID `gorm:"type:uuid;not null;index"`
	CourseID  uint      `gorm:"not null;index"`
	ModelPath string    `json:"model_path"`
	IsReady   bool      `json:"is_ready"`
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
