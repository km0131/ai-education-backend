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
	Id                string    `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	TeacherName       string    `json:"teacher_name"`
	StudentCount      int       `json:"student_count"`
	InviteCode        string    `json:"invite_code"`
	ThemeColor        string    `json:"theme_color"`
	UpdataTime        time.Time `json:"updata_time"`
	AiCreationBlocked bool      `json:"ai_creation_blocked"`
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
	TestStatus  string    `json:"test_status"`
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

// HEIC/RAWのバックグラウンド変換状況をまとめて問い合わせるためのリクエスト/レスポンス
type ConversionStatusRequest struct {
	PhotoIDs []uint `json:"photo_ids" binding:"required"`
}

type ConversionStatusEntry struct {
	PhotoID uint   `json:"photo_id"`
	Status  string `json:"status"` // ready / processing / failed
	Error   string `json:"error,omitempty"`
}

type ConversionStatusResponse struct {
	Statuses []ConversionStatusEntry `json:"statuses"`
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

// TestItinerary: テスト実行1回分の記録。モデル名 → 画像結果リストのマップ
type TestItinerary struct {
	StudentTestJobID uint                         `json:"student_test_job_id"`
	Models           map[string][]TestResultEntry `json:"models"`
}

type TestResultEntry struct {
	ImageID          uint    `json:"image_id"`
	Filename         string  `json:"filename"`
	TrueLabelID      int     `json:"true_label_id"`      // 正解ラベル（evaluate用）
	PredictedLabelID *int    `json:"predicted_label_id"` // Pythonが埋める（初期はnil）
	Confidence       float64 `json:"confidence"`         // Pythonが埋める（初期は0）
}

// TestModelSummary: Pythonが算出したモデルごとの集計結果
type TestModelSummary struct {
	Accuracy    float64 `json:"accuracy"`
	Loss        float64 `json:"loss"`
	TotalImages int     `json:"total_images"`
}

// TestResultCallbackInput: PythonからのテストResultコールバック（/api/callback/test_result）のボディ
// 成功時は Summary/Itinerary が入り、失敗時は Detail にエラー内容が入る
type TestResultCallbackInput struct {
	Status    string                      `json:"status" binding:"required"` // "success" or "error"
	StatusID  uint                        `json:"status_id" binding:"required"`
	Summary   map[string]TestModelSummary `json:"summary"`
	Itinerary TestItinerary               `json:"itinerary"`
	Detail    string                      `json:"detail"`
}

// PythonTestResultCallback はテスト実行結果を受け取るペイロード
type PythonTestResultCallback struct {
	Status   string `json:"status" binding:"required"`
	StatusID uint   `json:"status_id" binding:"required"`
	Summary  map[string]struct {
		Accuracy    float64 `json:"accuracy"`
		Loss        float64 `json:"loss"`
		TotalImages int     `json:"total_images"`
	} `json:"summary"`
	Itinerary struct {
		Models map[string][]TestResultEntry `json:"models"`
	} `json:"itinerary"`
	Detail string `json:"detail"` // エラー時のみ使用
}
