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

// FeatureType: クラスが使う機能種別のマスターテーブル(画像分類AI / Web開発環境 等)。
// Course.FeatureTypeID から参照される。ENUMカラムではなくマスターテーブル方式に
// しているのは、将来3つ目以降の機能拡張に備えるため。
type FeatureType struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Key       string    `gorm:"type:varchar(50);not null;unique" json:"key"` // コード内の識別子(例: "image_classification")
	Name      string    `gorm:"type:varchar(100);not null" json:"name"`      // 画面表示用の名称
	CreatedAt time.Time `json:"created_at"`
}

// feature_types.key の値。マジックストリングの重複を避けるための定数。
const (
	FeatureTypeKeyImageClassification = "image_classification"
	FeatureTypeKeyWebDev              = "web_dev"
)

// Course はクラス情報の永続化モデルです。
type Course struct {
	gorm.Model
	Title       string `gorm:"not null"`
	Description string
	InviteCode  string    `gorm:"unique;not null;index"`
	TeacherID   uuid.UUID `gorm:"type:uuid;not null;index"`
	Teacher     User      `gorm:"foreignKey:TeacherID;references:ID"`
	ThemeColor  string

	// 先生がこのクラスでのAI新規作成/学習開始/性能テストを一時的に停止しているかどうか。
	// クラス単位で管理する(グローバルなON/OFFではない)。
	AiCreationBlocked bool `gorm:"not null;default:false"`

	// クラスが使う機能種別(作成後は変更不可、変更したい場合はクラスを作り直す)。
	// 意図的に `not null` タグを付けていない: 既存クラスがある状態でAutoMigrateが
	// このカラムを追加する際、NOT NULLだと既存行がすべて弾かれて失敗するため。
	// NULL許可のまま追加→既存行を一括更新→NOT NULL化、という安全な3段階は
	// internal/db/client.go の Migrate() 内で生SQLとして実行する。
	FeatureTypeID uint        `gorm:"index"`
	FeatureType   FeatureType `gorm:"foreignKey:FeatureTypeID"`
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

// ProgramSandbox: 中高生向けプログラム学習機能のサンドボックスコンテナの
// 状態管理テーブル(NextPlan.md §3.4のルーティングテーブルに相当)。
// 1ユーザーにつき1クラス1環境(AI画像分類システムと同様の方針)。
// Docker自体が実際のコンテナ存在・状態の正とする(このテーブルはその写し・
// 履歴であり、DB側の記録だけを信用してDocker操作をスキップすることはしない)。
// DeletedAtがNULLの行が「現在有効な」サンドボックスを表す(削除時はソフト
// デリートし、以前の割り当て履歴を残す)。
type ProgramSandbox struct {
	gorm.Model
	UserID        uuid.UUID `gorm:"type:uuid;not null;index:idx_program_sandbox_user_course"`
	User          User      `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	CourseID      uint      `gorm:"not null;index:idx_program_sandbox_user_course"`
	Course        Course    `gorm:"foreignKey:CourseID;constraint:OnDelete:CASCADE"`
	ContainerID   string    `gorm:"type:varchar(64);not null"`
	ContainerName string    `gorm:"type:varchar(128);not null"`
	// 生徒が作成時に付ける表示名(Docker上の実コンテナ名(ContainerName)とは別物。
	// ContainerNameはuser_id+course_idから決定的に生成される内部識別用の名前で、
	// Dockerのホスト全体でのユニーク制約を守るため生徒が自由入力する対象にはしない)。
	Name string `gorm:"type:varchar(100);not null;default:''"`
	// running / stopped。DockerのContainerStateをそのまま転記する
	Status string `gorm:"type:varchar(20);not null;index"`
	// 外部公開されているか。公開自体の実処理(リバースプロキシ経由の外部アクセス、
	// NextPlan.md フェーズ7)は未実装のため、現時点では常にfalseのまま
	// (フィールドとしてはDB/APIに用意しておき、公開機能実装時にそのまま使う)。
	Published bool `gorm:"not null;default:false"`
	// PoolSlot: このサンドボックスに割り当てられたディスククォータプールの
	// スロット名(例: "pool-01")。ホスト側でループバックマウント済みのディレクトリ
	// (scripts/setup-sandbox-pool.sh で事前作成、NextPlan.md §6)に対応し、
	// コンテナ作成時に /root/workspace へbind mountする。空文字列 = 未割り当て
	// (プール枯渇時などStartProgramContainerがエラーで弾くため通常発生しない)。
	// 同時に使用中のスロットを求める際は「deleted_at IS NULLかつPoolSlot != ''」
	// の行で判定する(program_service.go の allocatePoolSlot 参照)。
	PoolSlot string `gorm:"type:varchar(32);not null;default:''"`
	// LastActiveAt: 生徒がこのサンドボックスへ最後に活動した時刻(作成/再開/
	// シェル・LSPチケット発行のたびに更新)。running かつ Published=false の
	// サンドボックスがこの時刻から SANDBOX_IDLE_TIMEOUT_MINUTES 以上経過すると、
	// アイドルタイムアウトの自動停止スイーパー
	// (internal/service/idle_sandbox_service.go)の対象になる(NextPlan.md フェーズ2)。
	// 公開中(Published=true)のコンテナは対象外(フェーズ7、§14「公開中のサスペンド除外」)。
	// default付き: DEFAULTが無いとNOT NULL列をAutoMigrateで既存行のあるテーブルへ
	// 追加する際「column contains null values」でALTER TABLE自体が失敗し(実際に
	// この既存行4件で起きた)、マイグレーションがエラーのまま握りつぶされて列が
	// 永久に作られない事故につながる。既存行はCURRENT_TIMESTAMPで埋める。
	LastActiveAt time.Time `gorm:"not null;index;default:CURRENT_TIMESTAMP"`

	// 以下、単一サブドメイン(preview.a-kiis.com)パスベース公開機能
	// (NextPlan.md フェーズ7)。Published=trueの間だけ意味を持つ - 公開停止/
	// 期限切れで降格した後もこれらの値自体は履歴として残すが(空文字列/ゼロ値
	// へは戻さない)、ルーティングは必ずPublished=trueも一緒に確認すること
	// (publish_service.goのFindSandboxByPublishSlug参照)。

	// PublishSlug: 公開URL(https://preview.a-kiis.com/{PublishSlug}/)の
	// パス先頭に使う、生徒のUserID(UUID)とは別のランダムな公開用識別子。
	// 本人のアカウントIDをそのまま公開URLに晒さないための使い捨てトークンで、
	// 公開するたびに新しく生成し直す(publish_service.goのgeneratePublishSlug)。
	PublishSlug string `gorm:"type:varchar(32);not null;default:'';index"`
	// PublishPort: 生徒のアプリが実際にlistenしているコンテナ内ポート
	// (例: Flaskの既定である5000)。公開開始時に生徒が指定する。
	PublishPort int `gorm:"not null;default:0"`
	// PublishExpiresAt: この時刻を過ぎると自動失効スケジューラ
	// (publish_service.goのStartPublishExpirySweeper)がPublished=falseへ
	// 降格させる。「公開期間を延長する」操作はこの時刻を後ろへずらすだけ。
	PublishExpiresAt time.Time `gorm:"not null;index;default:CURRENT_TIMESTAMP"`

	// Locked: 教師ダッシュボードの安全対策(緊急停止/再開ロック、
	// TeacherDashboardModal.tsx)。trueの間、生徒本人はこのコンテナを再開
	// できない(ResumeProgramContainerがErrSandboxLockedを返す、
	// program_service.goのrejectIfLocked参照) - 生徒が悪質な操作をした
	// 場合に、教師が「緊急停止」(docker kill)した上でこのロックを掛け、
	// 本人が勝手に再開して同じ操作を繰り返すのを防ぐ。ロック/解除は
	// 教師のみ行える(SetSandboxLocked、teacher_dashboard_service.go)。
	Locked bool `gorm:"not null;default:false"`
	// LockedReason: ロックした理由(教師が任意入力、空文字列可)。監査/
	// 経緯の記録用途のみで、ロック自体の可否判定には使わない。
	LockedReason string `gorm:"type:text;not null;default:''"`
}

// ContentReport: 公開プレビュー(preview.a-kiis.com)経由で閲覧できてしまう
// 生徒作成コンテンツについての通報受け口(NextPlan.md フェーズ7「悪用対策」)。
// 自動判定・自動非公開化は行わない(誤検知で生徒の公開を勝手に止めてしまう
// リスクを避けるため) - 教員が一覧を見て手動で対応する前提の記録テーブル。
type ContentReport struct {
	gorm.Model
	// ReporterUserID: 通報した人(ログイン済みユーザーのみ通報可能)。
	ReporterUserID uuid.UUID `gorm:"type:uuid;not null"`
	Reporter       User      `gorm:"foreignKey:ReporterUserID;constraint:OnDelete:CASCADE"`
	// PublishSlug: 通報対象の公開URL(ProgramSandbox.PublishSlug)。対象の
	// サンドボックスが既に公開停止/削除済みでも通報自体は記録として残す
	// (FK制約は張らない)。
	PublishSlug string `gorm:"type:varchar(32);not null;index"`
	Reason      string `gorm:"type:text;not null"`
	// Reviewed: 教員が対応済みにチェックしたか(現時点では対応用のUI/APIは
	// このタスクの範囲外 - フィールドとしてのみ用意しておく)。
	Reviewed bool `gorm:"not null;default:false"`
}

// ProgramContainerCard: クラス内のサンドボックス一覧表示用のDTO(1行=1生徒のコンテナ)。
type ProgramContainerCard struct {
	Name          string `json:"name"`
	ContainerName string `json:"container_name"`
	StudentName   string `json:"student_name"`
	Status        string `json:"status"`
	Published     bool   `json:"published"`
	// PublishSlug: 単一サブドメイン公開機能(NextPlan.md フェーズ7)の公開用
	// 識別子。生のslugはJSONに含めない(publish_handler.goのpublishURL()で
	// 完全なURLに組み立ててからPublishURLへ入れる、ハンドラー層の責務) -
	// DTO自体はDB行の写しに留める。
	PublishSlug string `json:"-"`
	// PublishURL: ハンドラー層(ListProgramContainers)がPublishSlugから
	// 組み立てる完全な公開URL。Published=falseの間は空文字列。
	PublishURL string `json:"publish_url"`
	// PublishExpiresAt: 残り公開時間のカウントダウン表示に使う
	// (Published=falseの間はゼロ値)。
	PublishExpiresAt time.Time `json:"publish_expires_at"`
	// LastActiveAt: 教師ダッシュボード(TeacherDashboardModal.tsx)の稼働状況
	// 表示に使う - 生徒一覧を見た時に「誰が実際に手を動かしているか」の目安
	// になる(idle_sandbox_service.goのアイドルタイムアウト判定と同じ値)。
	LastActiveAt time.Time `json:"last_active_at"`
	// Locked/LockedReason: 教師ダッシュボードの安全対策(緊急停止/再開ロック)。
	// Locked=trueの間、生徒本人はこのコンテナを再開できない
	// (ProgramSandbox.Lockedのコメント参照)。
	Locked       bool   `json:"locked"`
	LockedReason string `json:"locked_reason"`
	// UserID: 教師ダッシュボード(TeacherDashboardModal.tsx)が個別の公開停止/
	// 一括操作の対象を指定するのに使う。UUID自体は秘密情報ではなく
	// (これ単体では何の操作もできない - 実際の権限確認は常にAuthMiddleware
	// で認証済みの呼び出し元本人のIDとクラスのteacher_idの一致で行う)、
	// クラスに参加している全員が既にこの一覧自体を見られる前提と合わせて
	// JSONにそのまま含める。
	UserID uuid.UUID `json:"user_id"`
	IsMine bool      `json:"is_mine"`
}
