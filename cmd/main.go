package main // ← 必ず1行目！

import (
	"ai-education/backend/internal/worker"
	"log"
	"time"

	_ "ai-education/backend/docs" // 1. swag initで生成されるdocsをインポート

	"ai-education/backend/internal/controller"
	"ai-education/backend/internal/db"
	"ai-education/backend/internal/handler"
	"ai-education/backend/internal/utils"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @Summary      疎通確認
// @Description  サーバーの生存確認用
// @Tags         system
// @Accept       json
// @Produce      json
// @Success      200 {object} map[string]string
// @Router       /ping [get]
func PingHandler(c *gin.Context) {
	c.JSON(200, gin.H{"message": "Hello from Go Backend!"})
}

// @title           AI Education API
// @version         1.0
// @description     AI EducationのバックエンドAPIサーバーです。
// @host            localhost:8080
// @BasePath        /
func main() {

	db.InitDB()
	db.Migrate()

	worker.StartGPUWorker(db.DB)

	r := gin.Default()

	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// ハンドラーの初期化
	h := handler.Handler{
		DB: db.DB,
	}

	r.GET("/images/certification/*filename", h.PostPasswordImage)
	r.GET("/images/ai_photogrph/*filename", h.GetAiPhotographImage)
	r.GET("/images/test_photogrph/*filename", h.GetTestImage)
	r.GET("/storage/models/*filename", h.GetModelFile)
	r.POST("/api/callback/model_ready", utils.MachineToMachineAuth(), controller.HandleModelReady)
	r.POST("/api/callback/test_result", utils.MachineToMachineAuth(), controller.HandleTestReady)

	v0 := r.Group("/api/v0")
	{
		// ルーティング
		v0.POST("/", h.PostLogin)
		v0.GET("/signup", h.GetSignup)
		v0.POST("/signup", h.PostSignup)
		v0.POST("/login_registrer", h.PostLoginRegistrer)
		v0.POST("/login_qr", h.PostLoginQR)
	}

	v1 := r.Group("/api/v1")
	{
		// main関数の中のインライン定義ではなく、上で定義した関数を使う
		v1.GET("/ping", PingHandler)
		authGroup := v1.Group("/")
		authGroup.Use(utils.AuthMiddleware(h.DB))
		{
			authGroup.POST("/create_class", h.CreateClass)
			authGroup.POST("/join_class", h.JoinClass)
			authGroup.GET("/user", h.User)
			authGroup.GET("/my_courses", h.MyCourses)
			authGroup.GET("/courses/:id", h.RemoveClass)
			aiGroup := authGroup.Group("/ai")
			aiGroup.Use(utils.AuthMiddleware(h.DB))
			{
				aiGroup.POST("/upload_image", h.UploadImage)
				aiGroup.POST("/aicard", h.AiCard)
				aiGroup.POST("/ai_creation", h.AiCreation)
				aiGroup.POST("/get_description", h.GetDescription)
				aiGroup.PUT("/create_description", h.CreateDescription)
				aiGroup.POST("/image_acquisition", h.ImageAcquisition)
				aiGroup.POST("/image_updated", h.ImageUpdated)
				aiGroup.POST("/delete_image", h.DeleteImage)
				aiGroup.POST("/up_label", h.UpLabel)
				aiGroup.POST("/ai_model", h.AiModel)
				aiGroup.POST("/photo_status", h.PhotoStatus)
			}
			testGroup := authGroup.Group("/test")
			testGroup.Use(utils.AuthMiddleware(h.DB))
			{
				testGroup.POST("/uploading_test_image", h.UploadingTestImage)
				testGroup.POST("/get_images", h.GetImage)
				testGroup.POST("/delete_tsst_image", h.DeleteTestImage)
				testGroup.POST("/get_test_label", h.GetTestLabel)
				testGroup.POST("/get_test_label_options", h.GetTestLabelOptions)
				testGroup.POST("/up_test_label", h.UpTestLabel)
				testGroup.POST("/get_test_label_map", h.GetTestLabelMap)
				testGroup.POST("/up_test_label_map", h.UpStudentTestLabel)
				testGroup.POST("/execution", h.TestExecution)
				testGroup.POST("/photo_status", h.TestPhotoStatus)
			}
			resultGroup := authGroup.Group("/result")
			resultGroup.Use(utils.AuthMiddleware(h.DB))
			{
				resultGroup.POST("/training_curve", h.TrainingCurve)
				resultGroup.POST("/test_results", h.TestResults)
				resultGroup.POST("/test_results_imge", h.TestResultsImge)
				resultGroup.POST("/image_evaluation_get", h.ImageEvaluationGet)

			}
		}
	}

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	log.Println("Server listening on :8080")

	r.Run(":8080")
}
