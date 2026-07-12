package db

import (
	"ai-education/backend/internal/api"
	"ai-education/backend/internal/model"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// テストデータが存在しているかチェック
func TestDataCheck(db *gorm.DB, courseID uint, batchID uuid.UUID) (bool, error) {
	var count int64
	err := db.Model(&model.TestImage{}).Where("course_id = ? ", courseID).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("failed to check test data existence: %w", err)
	}
	// クラスIDのデータがまだ1件も無いなら、無条件で登録
	if count == 0 {
		return true, nil
	}
	// クラスIDが存在する場合、送られてきた batch_id がすでにそのクラスに含まれているか確認
	var batchCount int64
	err = db.Model(&model.TestImage{}).
		Where("course_id = ? AND batch_id = ?", courseID, batchID).
		Count(&batchCount).Error
	if err != nil {
		return false, fmt.Errorf("failed to check batch existence: %w", err)
	}
	// クラスIDが有り、かつ同じ batch_id のデータが1件以上あるなら登録OK
	if batchCount > 0 {
		return true, nil
	}
	// クラスIDが有るのに、同じ batch_id が見つからない（違うID）なら登録不可
	return false, fmt.Errorf("すでに登録されているので登録出来ません")
}

// テスト画像保存用DB
func CreatingTestDatasetDB(db *gorm.DB, courseID uint, imageURL string, batchID uuid.UUID, correctLabelName string) error {
	// トランザクション処理の開始
	err := db.Transaction(func(tx *gorm.DB) error {
		// 挿入するデータの構造体を組み立てる
		testImage := model.TestImage{
			CourseID:         courseID,
			ImageURL:         imageURL,
			BatchID:          batchID,
			CorrectLabelName: correctLabelName,
		}
		// DBにレコードを追加
		if err := tx.Create(&testImage).Error; err != nil {
			// エラーを返すと自動的にロールバックされます
			return err
		}
		return nil
	})
	return err
}

type TestImageResponse struct {
	ID       uint   `json:"id"`
	ImageURL string `json:"image_url"`
}

// テスト画像を取得
func GetImageDB(db *gorm.DB, courseID uint) (map[uint]map[uuid.UUID]map[string][]TestImageResponse, error) {
	var testImages []model.TestImage
	err := db.Where("course_id = ?", courseID).Find(&testImages).Error
	if err != nil {
		// それ以外の本物のDBエラー
		return nil, fmt.Errorf("failed to get test image: %w", err)
	}

	result := make(map[uint]map[uuid.UUID]map[string][]TestImageResponse)

	for _, img := range testImages {
		// 2. 第1階層（CourseID）の nil チェックと初期化
		if result[img.CourseID] == nil {
			result[img.CourseID] = make(map[uuid.UUID]map[string][]TestImageResponse)
		}

		// 3. 第2階層（BatchID）の nil チェックと初期化
		if result[img.CourseID][img.BatchID] == nil {
			result[img.CourseID][img.BatchID] = make(map[string][]TestImageResponse)
		}

		// 4. 正しく3つのキーを指定してデータを append
		result[img.CourseID][img.BatchID][img.CorrectLabelName] = append(
			result[img.CourseID][img.BatchID][img.CorrectLabelName],
			TestImageResponse{
				ID:       img.ID,
				ImageURL: img.ImageURL,
			},
		)
	}
	//　取得成功時は、構造体のポインタを返す
	return result, nil
}

// 画像削除
func DeleteImageDB(db *gorm.DB, ID int) error {
	if err := db.Where("id = ?", ID).Delete(&model.TestImage{}).Error; err != nil {
		return err
	}
	return nil
}

// リストを取得
func GetTestLabelDB(db *gorm.DB, courseID uint) ([]string, error) {
	var testImages []model.TestImage
	err := db.Where("course_id = ?", courseID).Find(&testImages).Error
	if err != nil {
		// DBエラー
		return nil, fmt.Errorf("failed to get test image: %w", err)
	}

	// map を使ってラベル名の重複を排除
	labelMap := make(map[string]bool)
	for _, img := range testImages {
		if img.CorrectLabelName != "" { // 空文字でなければマップに登録
			labelMap[img.CorrectLabelName] = true
		}
	}

	// マップのキーを取り出して、まとめられたリスト（スライス）を作成
	labels := make([]string, 0, len(labelMap))
	for label := range labelMap {
		labels = append(labels, label)
	}

	// まとめたリストと nil エラーを返す
	return labels, nil

}

// リストを変更
func UpTestLabelDB(db *gorm.DB, courseID uint, oldLabelName string, newLabelName string) (int64, error) {
	// 空文字への変更や、変更前後が同じ場合は処理をスキップ
	if newLabelName == "" || oldLabelName == newLabelName {
		return 0, nil
	}
	result := db.Model(&model.TestImage{}).
		Where("course_id = ? AND correct_label_name = ?", courseID, oldLabelName).
		Update("correct_label_name", newLabelName)

	if result.Error != nil {
		return 0, fmt.Errorf("failed to update test image labels: %w", result.Error)
	}
	log.Printf("[Info] ラベル変更完了: クラスID %d において '%s' から '%s' へ %d 件変更されました",
		courseID, oldLabelName, newLabelName, result.RowsAffected)

	// 影響のあった件数（何件書き換わったか）を返す
	return result.RowsAffected, nil
}

// テストラベルの中間テーブルを取得
func GetStudentTestMapping(db *gorm.DB, projectid uuid.UUID) ([]model.StudentTestMapping, error) {
	var testLabel []model.StudentTestMapping
	err := db.Where("project_uuid = ?", projectid).Find(&testLabel).Error
	if err != nil {
		log.Printf("[Info] ラベルの中間テーブルの取得に失敗しました。")
		return nil, err
	}
	return testLabel, nil
}

// ResolveStudentLabelFromTeacherLabel は、TestImageが持つ「先生の正解ラベル」から
// 生徒のAIモデルが本来出力すべき「生徒ラベルID（訓練時のラベル空間）」を逆引きします
func ResolveStudentLabelFromTeacherLabel(data *gorm.DB, projectID uuid.UUID, teacherLabel string) (int, error) {
	// StudentTestMappingを「先生ラベル→生徒ラベル」の向きで検索
	var mapping model.StudentTestMapping
	if err := data.Where("project_uuid = ? AND teacher_label_name = ?", projectID, teacherLabel).
		First(&mapping).Error; err != nil {
		return 0, fmt.Errorf("failed to find mapping for teacher label %s: %w", teacherLabel, err)
	}

	// AiCategoryを「生徒ラベル名→ラベルID（CategoryIndex）」の向きで検索
	var aiCategory model.AiCategory
	if err := data.Where("config_id = ? AND title = ?", projectID, mapping.StudentLabelName).
		First(&aiCategory).Error; err != nil {
		return 0, fmt.Errorf("failed to find ai category for student label %s: %w", mapping.StudentLabelName, err)
	}

	return aiCategory.CategoryIndex, nil
}

// 生徒と先生のラベルの中間テーブルをUP
func UpStudentTestLabelDB(db *gorm.DB, req model.SaveMappingRequest, userID uuid.UUID, teacher bool) error {
	if teacher == false {
		author, err := AuthorCheck(db, userID, req.ProjectUUID)
		if err != nil {
			log.Printf("[ERROR] AI作成の作成に失敗しました。: %v", err)
			return err
		}
		if !author {
			log.Printf("[ERROR] ラベル修正の権限が有りません。: %v", err)
			return fmt.Errorf("ラベル修正の権限が有りません")
		}
	}
	var dbMappings []model.StudentTestMapping
	for _, m := range req.Mappings {
		dbMappings = append(dbMappings, model.StudentTestMapping{
			ProjectUUID:      req.ProjectUUID,
			CourseID:         req.CourseID,
			StudentLabelName: m.StudentLabelName,
			TeacherLabelName: m.TeacherLabelName,
		})
	}
	err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_uuid"}, {Name: "student_label_name"}},  // 競合を判定するカラム
		DoUpdates: clause.AssignmentColumns([]string{"teacher_label_name", "updated_at"}), // 競合時に更新するカラム
	}).Create(&dbMappings).Error

	if err != nil {
		log.Printf("[Info] ラベルの中間テーブルの更新に失敗しました。")
		return err
	}

	return nil

}

// テストステータスを確認
func UpTestStatus(db *gorm.DB, projectID uuid.UUID, userID uuid.UUID) (*model.StudentTestJob, *time.Time, error) {
	var trainingJob model.AiTrainingJob
	// projectId (ConfigID) を元に、現在有効なAIモデル（IsCurrent = true）を取得する
	err := db.Where("config_id = ? AND is_current = ?", projectID, true).
		Order("version DESC"). // 万が一のため最新のもの
		First(&trainingJob).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.New("有効な学習済みモデル（バージョン）が見つかりません。先に学習を行ってください。")
		}
		return nil, nil, err
	}
	var existingJob model.StudentTestJob
	// 該当するモデル（TrainingJobID）で、現在実行中（running）のテストジョブがないか確認
	err = db.Where("training_job_id = ? AND status = ?", trainingJob.ID, "running").
		Order("updated_at DESC").
		First(&existingJob).Error
	// 💡 すでに running のジョブが存在する場合 ➔ そのまま構造体と UpdatedAt（UP時間）を返す
	if err == nil {
		return &existingJob, &existingJob.UpdatedAt, nil
	}
	// running のジョブがなければ、新規に "pending" ステータスで作成する
	newJob := model.StudentTestJob{
		UserID:        userID,
		TrainingJobID: trainingJob.ID,
		Status:        "pending", // 準備中
		TotalAccuracy: 0.0,
	}
	if err := db.Create(&newJob).Error; err != nil {
		return nil, nil, err
	}
	// 新規作成なので、UP時間は不要（nil）で返す
	return &newJob, nil, nil
}

// GetTestImagesByProject はプロジェクトに紐づくテスト画像一覧を取得します
func GetTestImagesByProject(db *gorm.DB, courseid uint) ([]model.TestImage, error) {
	var images []model.TestImage
	// project_id に紐づくテスト画像を全件取得
	err := db.Where("course_id = ?", courseid).Find(&images).Error
	return images, err
}

// GetModelPathsByTrainingJob は指定された学習ジョブのモデルのパスを取得します
func GetModelPathsByTrainingJob(db *gorm.DB, trainingJobID uuid.UUID) (*model.AiTrainingJob, error) {
	var job model.AiTrainingJob
	err := db.Where("config_id = ? AND is_current = ?", trainingJobID, true).First(&job).Error
	return &job, err
}

// テストのステータスをテスト中に変更
func TestStatusDB(db *gorm.DB, id uint) error {
	err := db.Model(&model.StudentTestJob{}).Where("id = ?", id).Update("status", "running").Error
	return err
}

// SaveTestResultDB はPythonからのテスト結果コールバックを保存します
// 失敗通知(status != "success")の場合は StudentTestJob を failed にするだけ、
// 成功時はモデル別集計(StudentTestJobModel)と画像単位のスナップショット(StudentTestResultSnapshot)を作成し、
// それらを集計して StudentTestJob.TotalAccuracy を更新します
func SaveTestResultDB(database *gorm.DB, input model.TestResultCallbackInput) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var job model.StudentTestJob
		if err := tx.First(&job, input.StatusID).Error; err != nil {
			return fmt.Errorf("failed to find student test job (id=%d): %w", input.StatusID, err)
		}

		if input.Status != "success" {
			return tx.Model(&job).Updates(map[string]interface{}{
				"status":        "failed",
				"error_message": input.Detail,
			}).Error
		}

		var totalCorrect, totalImages int
		for modelName, summary := range input.Summary {
			jobModel := model.StudentTestJobModel{
				StudentTestJobID: job.ID,
				ModelName:        modelName,
				Accuracy:         summary.Accuracy,
				Loss:             summary.Loss,
				TotalImages:      summary.TotalImages,
			}
			if err := tx.Create(&jobModel).Error; err != nil {
				return fmt.Errorf("failed to create test job model (%s): %w", modelName, err)
			}

			entries := input.Itinerary.Models[modelName]
			snapshots := make([]model.StudentTestResultSnapshot, 0, len(entries))
			for _, entry := range entries {
				if entry.PredictedLabelID == nil {
					continue
				}
				isCorrect := *entry.PredictedLabelID == entry.TrueLabelID
				snapshots = append(snapshots, model.StudentTestResultSnapshot{
					StudentTestJobModelID: jobModel.ID,
					TestImageID:           entry.ImageID,
					PredictedLabelID:      *entry.PredictedLabelID,
					Confidence:            entry.Confidence,
					IsCorrect:             isCorrect,
				})
				if isCorrect {
					totalCorrect++
				}
				totalImages++
			}
			if len(snapshots) > 0 {
				if err := tx.Create(&snapshots).Error; err != nil {
					return fmt.Errorf("failed to create test result snapshots (%s): %w", modelName, err)
				}
			}
		}

		var totalAccuracy float64
		if totalImages > 0 {
			totalAccuracy = float64(totalCorrect) / float64(totalImages)
		}

		return tx.Model(&job).Updates(map[string]interface{}{
			"status":         "success",
			"total_accuracy": totalAccuracy,
		}).Error
	})
}

// PhotographEvaluation: 画像1枚分の評価情報
type PhotographEvaluation struct {
	PhotographPath  string           `json:"photograph_path"`
	Saturation      float64          `json:"saturation"`
	Brightness      float64          `json:"brightness"`
	Sharpness       float64          `json:"sharpness"`
	DiversityVector model.FloatSlice `json:"diversity_vector"`
	IsAnalyzed      bool             `json:"is_analyzed"`
}

// CategoryEvaluationSummary: ラベル(カテゴリ)単位の平均評価と画像一覧
type CategoryEvaluationSummary struct {
	CategoryID             uuid.UUID              `json:"category_id"`
	CategoryIndex          int                    `json:"category_index"`
	Title                  string                 `json:"title"`
	Explanation            string                 `json:"explanation"`
	AverageSaturation      float64                `json:"average_saturation"`
	AverageBrightness      float64                `json:"average_brightness"`
	AverageSharpness       float64                `json:"average_sharpness"`
	AverageDiversityVector model.FloatSlice       `json:"average_diversity_vector"`
	PhotographCount        int                    `json:"photograph_count"`
	Photographs            []PhotographEvaluation `json:"photographs"`
}

type OverallAverage struct {
	Saturation             float64          `json:"saturation"`
	Brightness             float64          `json:"brightness"`
	Sharpness              float64          `json:"sharpness"`
	AverageDiversityVector model.FloatSlice `json:"average_diversity_vector"`
	PhotographCount        int              `json:"photograph_count"`
}

// averageVectors: 複数のベクトルを要素ごとに平均する（次元は最初の非空ベクトルに合わせる）
func averageVectors(sum []float64, count int) model.FloatSlice {
	if len(sum) == 0 || count == 0 {
		return nil
	}
	avg := make(model.FloatSlice, len(sum))
	for i, v := range sum {
		avg[i] = v / float64(count)
	}
	return avg
}

// addVector: dst に src を要素ごとに加算する（dst が未初期化なら src の長さで確保する）
func addVector(dst []float64, src model.FloatSlice) []float64 {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make([]float64, len(src))
	}
	for i, v := range src {
		if i < len(dst) {
			dst[i] += v
		}
	}
	return dst
}

// ImageEvaluationSummary: プロジェクト全体の評価サマリー
type ImageEvaluationSummary struct {
	ProjectID      uuid.UUID                   `json:"project_id"`
	JobID          uint                        `json:"job_id"`
	JobStatus      string                      `json:"job_status"`
	OverallAverage OverallAverage              `json:"overall_average"`
	Categories     []CategoryEvaluationSummary `json:"categories"`
}

// GetImageEvaluationDB: projectID に紐づく画像評価をラベル別・全体でまとめて取得する
func GetImageEvaluationDB(database *gorm.DB, projectID uuid.UUID) (*ImageEvaluationSummary, error) {
	var categories []model.AiCategory

	err := database.
		Preload("Photographs").
		Where("config_id = ?", projectID).
		Order("created_at asc").
		Find(&categories).Error
	if err != nil {
		return nil, fmt.Errorf("画像評価情報の取得に失敗しました: %w", err)
	}

	// アップロードのたびに履歴として新しい AiCategory 行が作られる（CreateCategoryWithHistory）ため、
	// 同じタイトルのラベルが複数行に分かれている。タイトルごとに1つのグループへまとめる。
	type titleGroup struct {
		categoryID    uuid.UUID
		categoryIndex int
		explanation   string
		photographs   []model.AiPhotograph
	}
	groups := make(map[string]*titleGroup)
	titleOrder := make([]string, 0)

	for _, category := range categories {
		g, ok := groups[category.Title]
		if !ok {
			g = &titleGroup{}
			groups[category.Title] = g
			titleOrder = append(titleOrder, category.Title)
		}
		// created_at 昇順で読んでいるので、後から出てくるものほど最新の情報になる
		g.categoryID = category.CategoryID
		g.categoryIndex = category.CategoryIndex
		g.explanation = category.Explanation
		g.photographs = append(g.photographs, category.Photographs...)
	}

	var job model.AiTrainingJob
	err = database.
		Where("config_id = ? AND is_current = ?", projectID, true).
		First(&job).Error
	if err != nil {
		return nil, fmt.Errorf("JOB画像評価情報の取得に失敗しました: %w", err)
	}

	summary := &ImageEvaluationSummary{
		ProjectID: projectID,
		JobID:     job.ID,
		// JobStatus:  job.Status, // ※ AiTrainingJob の実フィールド名に合わせて調整
		Categories: make([]CategoryEvaluationSummary, 0, len(titleOrder)),
	}

	var (
		totalSaturation   float64
		totalBrightness   float64
		totalSharpness    float64
		totalDiversitySum []float64
		totalCount        int
	)

	for _, title := range titleOrder {
		g := groups[title]
		catSummary := CategoryEvaluationSummary{
			CategoryID:      g.categoryID,
			CategoryIndex:   g.categoryIndex,
			Title:           title,
			Explanation:     g.explanation,
			Photographs:     make([]PhotographEvaluation, 0, len(g.photographs)),
			PhotographCount: len(g.photographs),
		}

		var (
			catSaturation   float64
			catBrightness   float64
			catSharpness    float64
			catDiversitySum []float64
		)

		for _, photo := range g.photographs {
			catSummary.Photographs = append(catSummary.Photographs, PhotographEvaluation{
				PhotographPath:  photo.PhotographPath,
				Saturation:      photo.Saturation,
				Brightness:      photo.Brightness,
				Sharpness:       photo.Sharpness,
				DiversityVector: photo.DiversityVector,
				IsAnalyzed:      photo.IsAnalyzed,
			})

			catSaturation += photo.Saturation
			catBrightness += photo.Brightness
			catSharpness += photo.Sharpness
			catDiversitySum = addVector(catDiversitySum, photo.DiversityVector)
		}

		if catSummary.PhotographCount > 0 {
			n := float64(catSummary.PhotographCount)
			catSummary.AverageSaturation = catSaturation / n
			catSummary.AverageBrightness = catBrightness / n
			catSummary.AverageSharpness = catSharpness / n
			catSummary.AverageDiversityVector = averageVectors(catDiversitySum, catSummary.PhotographCount)
		}

		totalSaturation += catSaturation
		totalBrightness += catBrightness
		totalSharpness += catSharpness
		totalDiversitySum = addVector(totalDiversitySum, model.FloatSlice(catDiversitySum))
		totalCount += catSummary.PhotographCount

		summary.Categories = append(summary.Categories, catSummary)
	}

	if totalCount > 0 {
		n := float64(totalCount)
		summary.OverallAverage = OverallAverage{
			Saturation:             totalSaturation / n,
			Brightness:             totalBrightness / n,
			Sharpness:              totalSharpness / n,
			AverageDiversityVector: averageVectors(totalDiversitySum, totalCount),
			PhotographCount:        totalCount,
		}
	}

	reduceDiversityVectors(summary)

	return summary, nil
}

// reduceDiversityVectors: 各Photographの高次元diversity_vectorをPythonの/reduce_diversityへ
// まとめて送り、PCAで2次元化した結果に差し替える(表示リクエストのたびに計算し直す。永続化しない)。
// 過去に別次元数で解析されたデータが混在する可能性があるため、最も多い次元数のベクトルのみを対象にし、
// 対象外(未解析・次元不一致)のPhotographsは原点(0,0)にフォールバックする。
// Python側呼び出しが失敗した場合は元の高次元ベクトルのまま返す(致命的エラーにはしない)。
func reduceDiversityVectors(summary *ImageEvaluationSummary) {
	type vectorRef struct {
		catIdx   int
		photoIdx int
	}

	lengthCounts := make(map[int]int)
	for _, cat := range summary.Categories {
		for _, p := range cat.Photographs {
			if len(p.DiversityVector) > 0 {
				lengthCounts[len(p.DiversityVector)]++
			}
		}
	}
	majorityLen, majorityCount := 0, 0
	for length, count := range lengthCounts {
		if count > majorityCount {
			majorityLen, majorityCount = length, count
		}
	}
	if majorityLen == 0 {
		return
	}

	var refs []vectorRef
	var vectors [][]float64
	for ci, cat := range summary.Categories {
		for pi, p := range cat.Photographs {
			if len(p.DiversityVector) == majorityLen {
				refs = append(refs, vectorRef{catIdx: ci, photoIdx: pi})
				vectors = append(vectors, []float64(p.DiversityVector))
			}
		}
	}

	points, err := api.CallPythonReduceDiversityAPI(vectors)
	if err != nil {
		log.Printf("[WARN] diversity_vectorの2次元化に失敗しました(元の高次元ベクトルのまま返します): %v", err)
		return
	}

	for i, ref := range refs {
		if i < len(points) {
			summary.Categories[ref.catIdx].Photographs[ref.photoIdx].DiversityVector = model.FloatSlice(points[i])
		}
	}
	// PCA対象外(未解析・次元不一致)のPhotographsは2次元の原点にフォールバックする
	for ci, cat := range summary.Categories {
		for pi, p := range cat.Photographs {
			if len(p.DiversityVector) != 2 {
				summary.Categories[ci].Photographs[pi].DiversityVector = model.FloatSlice{0, 0}
			}
		}
	}

	// 2次元化後の値でカテゴリ別・全体の平均を再計算する
	var overallSum []float64
	var overallCount int
	for ci, cat := range summary.Categories {
		var catSum []float64
		for _, p := range cat.Photographs {
			catSum = addVector(catSum, p.DiversityVector)
		}
		summary.Categories[ci].AverageDiversityVector = averageVectors(catSum, len(cat.Photographs))
		overallSum = addVector(overallSum, model.FloatSlice(catSum))
		overallCount += len(cat.Photographs)
	}
	summary.OverallAverage.AverageDiversityVector = averageVectors(overallSum, overallCount)
}
