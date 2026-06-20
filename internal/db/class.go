package db

import (
	"ai-education/backend/internal/model"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TeacherClassSearch は先生のIDに紐づくクラス一覧をDBから取得します
func TeacherClassSearch(db *gorm.DB, teacherID uuid.UUID) ([]model.ClassSend, error) {
	var results []model.ClassSend

	// teacher_id が一致するレコードを全件取得
	err := db.Model(&model.Course{}).
		Select(`
            courses.id AS id,
            courses.title AS title,
            courses.description AS description,
            SPLIT_PART(users.name, '-', 1) AS teacher_name, -- users テーブルから先生の名前を結合
            courses.invite_code AS invite_code,
            courses.theme_color AS theme_color,
            courses.updated_at AS updata_time, -- カラム名を構造体にマッピング
            (SELECT COUNT(*) FROM course_enrollments WHERE course_enrollments.course_id = courses.id AND course_enrollments.deleted_at IS NULL) AS student_count -- サブクエリで人数をカウント
        `).
		Joins("JOIN users ON users.id = courses.teacher_id"). // 先生の情報を結合
		Where("courses.teacher_id = ? AND courses.deleted_at IS NULL", teacherID).
		Order("courses.updated_at DESC"). // 最新順
		Scan(&results).Error

	return results, err
}

// ClassTeacherSearch は生徒のIDに紐づくクラス一覧をDBから取得します
func ClassTeacherSearch(db *gorm.DB, studentID uuid.UUID) ([]model.ClassSend, error) {
	var results []model.ClassSend

	err := db.Model(&model.Course{}).
		Select(`
            courses.id AS id,
            courses.title AS title,
            courses.description AS description,
            SPLIT_PART(users.name, '-', 1) AS teacher_name, -- クラスを作った先生の名前
            courses.invite_code AS invite_code,
            courses.theme_color AS theme_color,
            courses.updated_at AS updata_time,
            (SELECT COUNT(*) FROM course_enrollments WHERE course_enrollments.course_id = courses.id AND course_enrollments.deleted_at IS NULL) AS student_count
        `).
		// 🌟 中間テーブル（course_enrollments）を経由して、自分が参加しているクラスを絞り込む
		Joins("JOIN course_enrollments ON course_enrollments.course_id = courses.id").
		Joins("JOIN users ON users.id = courses.teacher_id"). // 先生の名前取得用
		Where("course_enrollments.user_id = ? AND course_enrollments.deleted_at IS NULL AND courses.deleted_at IS NULL", studentID).
		Order("course_enrollments.created_at DESC"). // 参加したのが新しい順
		Scan(&results).Error

	return results, err
}

// 参加コードからクラスを検索
func ClassSearch(db *gorm.DB, InviteCode string) (*model.Course, error) {
	var course model.Course
	// invite_code が一致するレコードを全件取得
	if err := db.Where("invite_code = ?", InviteCode).Find(&course).Error; err != nil {
		return nil, err
	}

	return &course, nil
}

var ErrAlreadyJoined = errors.New("already joined this course")

// RegisterStudentToCourse は生徒をクラスに登録します（二重参加チェック付き）
func RegisterStudentToCourse(tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	// 1. 二重参加チェック
	var count int64
	err := tx.Model(&model.CourseEnrollment{}).
		Where("user_id = ? AND course_id = ?", userID, courseID).
		Count(&count).Error
	if err != nil {
		return err
	}

	if count > 0 {
		return ErrAlreadyJoined
	}

	// 2. 中間テーブルにレコードを挿入して参加処理
	studentCourse := model.CourseEnrollment{
		UserID:   userID,
		CourseID: courseID,
	}

	return tx.Create(&studentCourse).Error
}

// CreateCourse は招待コードを自動生成し、新しいクラスをDBに保存します
func CreateCourse(tx *gorm.DB, title string, description string, teacherID uuid.UUID) (*model.Course, error) {
	// 色ランダム
	colors := []string{
		"blue",
		"green",
		"indigo",
		"purple",
		"pink",
		"orange",
		"emerald",
		"cyan",
	}
	randomIndex := rand.IntN(len(colors))
	randomColor := colors[randomIndex]
	// 招待コードの生成
	var course *model.Course
	var err error

	// 🌟 最大10回まで再生成を試みるループ
	for i := 0; i < 10; i++ {
		inviteCode, genErr := GenerateRandomSuffix(6)
		if genErr != nil {
			return nil, genErr
		}

		course = &model.Course{
			Title:       title,
			Description: description,
			InviteCode:  inviteCode,
			TeacherID:   teacherID,
			ThemeColor:  randomColor,
		}

		// DBに挿入を試みる
		err = tx.Create(course).Error

		// 重複エラーかどうかをチェック
		if err != nil {
			// GORMのチェック: 23505 は PostgreSQL のユニーク制約違反コード
			if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "23505") {
				continue // 重複なら次のループで再生成
			}
			return nil, err // 重複以外のエラーなら即座に終了
		}

		// 成功したらループを抜ける
		return course, nil
	}

	return nil, fmt.Errorf("招待コードの生成に失敗しました（再試行上限に達しました）")
}

// GetClassDetailsForUser は指定された classID の詳細を取得します。
func GetClassDetailsForUser(db *gorm.DB, classID string, userID uuid.UUID) (*model.ClassSend, error) {
	var result model.ClassSend

	// SQLまたはGORMのRawクエリで所属チェックを掛けつつ一撃で取得
	err := db.Model(&model.Course{}).
		Select(`
			courses.id AS id,
			courses.title AS title,
			courses.description AS description,
			SPLIT_PART(users.name, '-', 1) AS teacher_name,
			courses.invite_code AS invite_code,
			courses.theme_color AS theme_color,
			courses.updated_at AS updata_time,
			(SELECT COUNT(*) FROM course_enrollments WHERE course_enrollments.course_id = courses.id AND course_enrollments.deleted_at IS NULL) AS student_count
		`).
		Joins("JOIN users ON users.id = courses.teacher_id"). // 先生の名前取得用
		// 左外部結合で中間テーブルを繋ぎ、自分が「先生」か「参加生徒」のどちらかならヒットさせる
		Joins("LEFT JOIN course_enrollments ON course_enrollments.course_id = courses.id AND course_enrollments.user_id = ? AND course_enrollments.deleted_at IS NULL", userID).
		// クラスのIDが一致、かつ（自分が作ったクラス、または自分が参加しているクラス）に絞る
		Where("courses.id = ? AND courses.deleted_at IS NULL AND (courses.teacher_id = ? OR course_enrollments.id IS NOT NULL)", classID, userID).
		Scan(&result).Error

	if err != nil {
		return nil, err
	}

	// レコードが見つからなかった（＝アクセス権がない、またはIDが間違っている）場合
	if result.Id == "" {
		return nil, errors.New("unauthorized_or_not_found")
	}

	return &result, nil
}
