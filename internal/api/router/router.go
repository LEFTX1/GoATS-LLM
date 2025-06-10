package router

import (
	"ai-agent-go/internal/api/handler"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/storage"
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/hertz-contrib/keyauth"
)

// RegisterRoutes 注册所有API路由
func RegisterRoutes(r *server.Hertz, resumeHandler *handler.ResumeHandler, jobSearchHandler *handler.JobSearchHandler, storage *storage.Storage, cfg *config.Config) {
	// 健康检查
	r.GET("/health", func(c context.Context, ctx *app.RequestContext) {
		ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// API V1 分组
	apiV1 := r.Group("/api/v1")

	// -- 简历相关路由 --
	resumeRoutes := apiV1.Group("/resume")
	{
		resumeRoutes.POST("/upload", resumeHandler.HandleResumeUpload) // POST /api/v1/resume/upload
		// 可以添加其他简历相关的路由，例如获取简历状态、获取解析结果等
		// resumeRoutes.GET("/:id/status", resumeHandler.HandleGetResumeStatus)
		// resumeRoutes.GET("/:id/parsed", resumeHandler.HandleGetParsedResume)
	}

	// 定义 KeyAuth 中间件
	authMiddleware := keyauth.New(
		keyauth.WithContextKey("hr_id"),
		keyauth.WithKeyLookUp("header:X-Hr-Id", ""),
		keyauth.WithValidator(func(ctx context.Context, requestContext *app.RequestContext, s string) (bool, error) {
			// s 就是从 header:X-Hr-Id 中提取的 hr_id
			var count int64
			err := storage.MySQL.DB().WithContext(ctx).Table("hrs").Where("hr_id = ?", s).Count(&count).Error
			if err != nil {
				// 数据库查询出错，拒绝访问
				return false, err
			}
			// 如果 count > 0，说明 hr_id 有效
			return count > 0, nil
		}),
		keyauth.WithErrorHandler(func(ctx context.Context, c *app.RequestContext, err error) {
			c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "Unauthorized",
				"message": "Invalid or missing HR ID",
			})
			c.Abort()
		}),
	)

	// -- 岗位与搜索相关路由 (需要认证) --
	jobRoutes := apiV1.Group("/jobs")
	jobRoutes.Use(authMiddleware) // 对所有 /jobs/* 路由应用认证中间件
	{
		// 新增：通过 JobID 搜索简历
		jobRoutes.GET("/:job_id/resumes/search", jobSearchHandler.HandleSearchResumesByJobID)

		// 新增：查询搜索状态
		jobRoutes.GET("/:job_id/search/status", jobSearchHandler.HandleCheckSearchStatus)

		// 新增：获取指定 JobID 的描述（辅助接口）
		jobRoutes.GET("/:job_id/description", jobSearchHandler.HandleGetJobDescription)

		// 新增：分页获取简历详情
		resumePaginatedHandler := handler.NewResumePaginatedHandler(cfg, storage)
		jobRoutes.GET("/:job_id/resumes/paginated", resumePaginatedHandler.HandlePaginatedResumeSearch)

		// TODO: 未来可以添加创建/更新/获取岗位信息的接口
		// jobRoutes.POST("/", jobHandler.HandleCreateJob)
		// jobRoutes.GET("/:job_id", jobHandler.HandleGetJob)
	}

	// WebSocket 路由 (如果需要)
	// r.GET("/ws", handler.WebSocketHandler())

	// 其他可能的路由...

	fmt.Println("路由注册完成")
}
