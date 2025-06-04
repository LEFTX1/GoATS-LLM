package router

import (
	"ai-agent-go/internal/api/handler" // 导入移动后的 handler 包

	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

// RegisterRoutes 注册所有API路由
func RegisterRoutes(r *server.Hertz, resumeHandler *handler.ResumeHandler, jobSearchHandler *handler.JobSearchHandler) {
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

	// -- 岗位与搜索相关路由 --
	jobRoutes := apiV1.Group("/jobs")
	{
		// 新增：通过 JobID 搜索简历
		jobRoutes.GET("/:job_id/resumes/search", jobSearchHandler.HandleSearchResumesByJobID)

		// 新增：获取指定 JobID 的描述（辅助接口）
		jobRoutes.GET("/:job_id/description", jobSearchHandler.HandleGetJobDescription)

		// TODO: 未来可以添加创建/更新/获取岗位信息的接口
		// jobRoutes.POST("/", jobHandler.HandleCreateJob)
		// jobRoutes.GET("/:job_id", jobHandler.HandleGetJob)
	}

	// WebSocket 路由 (如果需要)
	// r.GET("/ws", handler.WebSocketHandler())

	// 其他可能的路由...

	fmt.Println("路由注册完成")
}
