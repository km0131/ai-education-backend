package model

import "time"

// RegisterRequest はユーザー登録APIのリクエストボディです。
type RegisterRequest struct {
	Username       string `json:"username" binding:"required"`
	Role           string `json:"role" binding:"required"`
	Images         []int  `json:"images" binding:"required"`
	ImagesOriginal []int  `json:"image_original" binding:"required"`
	Email          string `json:"email"`
}

// TokenClaims は Paseto トークンのペイロード構造です。
type TokenClaims struct {
	UserID       uint      `json:"user_id"`
	Username     string    `json:"username"`
	ImageNumbers []int     `json:"image_numbers,omitempty"`
	IssuedAt     time.Time `json:"iat"`
	ExpiresAt    time.Time `json:"exp"`
}

// LoginRequest はログインAPIのリクエストボディです。
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Images   []int  `json:"images" binding:"required"`
}

// フロントエンドから送られてくるJSONをマッピングする構造体
type CreateClassInput struct {
	ClassName   string `json:"className" binding:"required"`
	Description string `json:"description"`
}

// クラス参加用
type CreateClassOutput struct {
	InviteCode string `json:"inviteCode" binding:"required"`
}

// クラス送信用
type ClassSend struct {
	Id           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	TeacherName  string    `json:"teacher_name"`
	StudentCount int       `json:"student_count"`
	InviteCode   string    `json:"invite_code"`
	ThemeColor   string    `json:"theme_color"`
	UpdataTime   time.Time `json:"updata_time"`
}

// 画像アップロード時のリクエスト構造体（multipart用とは別にJSONとして扱う場合）
type ImageUploadRequest struct {
	CourseID   uint   `form:"course_id" binding:"required"`
	CategoryID uint   `form:"category_id" binding:"required"`
	Title      string `form:"title" binding:"required"`
}

type AnalysisData struct {
	// GORM用のIDが必要な場合、gorm.Modelを埋め込むか明示的に定義
	PhotoID         int        `json:"photo_id" gorm:"primaryKey"`
	Saturation      float64    `json:"saturation" gorm:"type:float"`
	Brightness      float64    `json:"brightness" gorm:"type:float"`
	Sharpness       float64    `json:"sharpness" gorm:"type:float"`
	DiversityVector FloatSlice `json:"diversity_vector" gorm:"type:jsonb"` // 上記のFloatSlice型を利用
	Message         string     `json:"message"`
}
