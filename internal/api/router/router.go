package router

import (
	"ai-agent-go/internal/api/handler" // 导入移动后的 handler 包
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// RegisterRoutes 注册 API 路由
func RegisterRoutes(h *server.Hertz, resumeHandler *handler.ResumeHandler) {
	api := h.Group("/api/v1")

	api.POST("/resume/upload", func(c context.Context, ctx *app.RequestContext) {
		// 获取上传的文件
		fileHeader, err := ctx.FormFile("file")
		if err != nil {
			ctx.JSON(consts.StatusBadRequest, utils.H{"error": "文件未找到"})
			return
		}

		// 获取目标岗位ID
		targetJobID := ctx.PostForm("target_job_id")
		// 获取来源渠道
		sourceChannel := ctx.PostForm("source_channel")
		if sourceChannel == "" {
			sourceChannel = "web_upload" // 默认值
		}

		// 打开文件
		file, err := fileHeader.Open()
		if err != nil {
			ctx.JSON(consts.StatusInternalServerError, utils.H{"error": "打开文件失败"})
			return
		}
		defer file.Close()

		// 处理上传
		resp, err := resumeHandler.HandleResumeUpload(
			c,
			file,
			fileHeader.Size,
			fileHeader.Filename,
			targetJobID,
			sourceChannel,
		)
		if err != nil {
			ctx.JSON(consts.StatusInternalServerError, utils.H{"error": err.Error()})
			return
		}

		ctx.JSON(consts.StatusOK, resp)
	})

	// 添加健康检查
	api.GET("/health", func(c context.Context, ctx *app.RequestContext) {
		ctx.JSON(consts.StatusOK, utils.H{"status": "ok"})
	})
}
