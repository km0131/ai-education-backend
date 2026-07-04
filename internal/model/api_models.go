package model

import (
	"time"

	"github.com/google/uuid"
)

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
	CourseID        uint   `form:"course_id" binding:"required"`
	CategoryID      uint   `form:"category_id" binding:"required"`
	CategoryTitle   string `form:"category_title" binding:"required"`
	Title           string `form:"title" binding:"required"`
	UploadSessionID string `form:"upload_session_id" binding:"required"`
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

// Ai作成用情報の送信
type CategoryRequest struct {
	ProjectUUID uuid.UUID `json:"project_uuid" binding:"required"`
	Title       string    `json:"title" binding:"required"`
	StudentName string    `json:"student_name" binding:"required"`
	Status      string    `json:"status" binding:"required"`
}

// AIカード
type AiCard struct {
	ProjectUUID uuid.UUID `json:"project_uuid" binding:"required"`
	Title       string    `json:"title" binding:"required"`
	StudentName string    `json:"student_name" binding:"required"`
	Status      string    `json:"status" binding:"required"`
	UpdatedAt   time.Time `json:"updated_at"`
	Version     int       `json:"version"`
}

// PythonからAiを受け取る
type ModelReadyInput struct {
	JobID          uint    `form:"job_id" binding:"required"`
	AvgSaturation  float64 `form:"avg_saturation"`
	DiversityScore float64 `form:"diversity_score"`
	Accuracy       string  `form:"accuracy"`
	LearningCurve  string  `form:"learning_curve" binding:"required"`
}

// ラベルの送信
type CategoryResponse struct {
	CategoryIndex int    `json:"category_index"`
	Title         string `json:"title"`
	Explanation   string `json:"explanation"`
}

// PhotoInfo はフロントに返す各画像のミニマルな情報です
type PhotoInfo struct {
	ID   uint   `json:"id"`
	Path string `json:"path"`
}

// CategoryPhotosMap はカテゴリIDをキーとした、タイトルと画像リストの構造です
type CategoryPhotosMap struct {
	CategoryID    string      `json:"category_id"`
	CategoryIndex uint        `json:"category_index"`
	Title         string      `json:"title"`
	Photos        []PhotoInfo `json:"photos"`
}

// テスト画像送信
type ImageUploadResponse struct {
	CourseID         uint   `form:"course_id" json:"course_id"`
	BatchID          string `form:"batch_id" json:"batch_id"`
	CorrectLabelName string `form:"correct_label_name" json:"correct_label_name"`
}

// ラベルリスト返却
type TestLabelsResponse struct {
	Labels []string `json:"labels"`
}

type LabelMappingInput struct {
	StudentLabelName string `json:"student_label_name" binding:"required"`
	TeacherLabelName string `json:"teacher_label_name"` // 空白を許容する場合はbindingを外す
}

type SaveMappingRequest struct {
	ProjectUUID uuid.UUID           `json:"project_uuid" binding:"required"`
	CourseID    uint                `json:"course_id" binding:"required"`
	Mappings    []LabelMappingInput `json:"mappings" binding:"required"`
}

// 生徒と先生ラベルのリンクを返却
type MappingResponse struct {
	StudentLabelName string `json:"student_label_name"`
	TeacherLabelName string `json:"teacher_label_name"`
}

// Pythonの推論コードが読み込む、結果記録用のJSONエントリ
type TestResultEntry struct {
	ImageID          uint    `json:"image_id"`
	Filename         string  `json:"filename"`
	PredictedLabelID *int    `json:"predicted_label_id"` // 💡 Pythonに書き込んでもらうため初期値はnil
	Confidence       float64 `json:"confidence"`         // 💡 初期値 0.0
}

// TestItinerary: test_itinerary.json 全体の構造
type TestItinerary struct {
	StatusID int               `json:"status_id"` // 💡 ここにStudentTestJobのIDを入れる
	Results  []TestResultEntry `json:"results"`   // 画像ごとのリスト
}
