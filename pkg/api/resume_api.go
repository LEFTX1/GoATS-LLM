package api

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"ai-agent-go/pkg/handler" // 引入业务逻辑处理器

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// ResumeAPIHandler 负责处理简历相关的API请求
type ResumeAPIHandler struct {
	resumeHandler *handler.ResumeHandler
}

// NewResumeAPIHandler 创建一个新的 ResumeAPIHandler
func NewResumeAPIHandler(resumeHandler *handler.ResumeHandler) *ResumeAPIHandler {
	return &ResumeAPIHandler{
		resumeHandler: resumeHandler,
	}
}

// UploadResume 处理简历上传的API请求
// POST /api/v1/resume/upload
// FormData: resume_file (file), target_job_id (string, optional), source_channel (string, optional)
func (h *ResumeAPIHandler) UploadResume(ctx context.Context, c *app.RequestContext) {
	// 1. 获取上传的文件
	fileHeader, err := c.FormFile("resume_file")
	if err != nil {
		log.Printf("获取上传文件失败: %v", err)
		c.JSON(http.StatusBadRequest, map[string]string{"error": "无效的文件上传请求: 未找到 resume_file"})
		return
	}

	// 2. 获取其他表单参数
	// 如果这些参数不是必须的，可以提供默认值或进行更宽松的检查
	targetJobID := c.PostForm("target_job_id")
	sourceChannel := c.PostForm("source_channel")
	if sourceChannel == "" {
		sourceChannel = "API_UPLOAD" // 如果未提供，则设置默认来源
	}

	log.Printf("收到简历上传请求: 文件名=%s, 大小=%.2f KB, 目标岗位ID=%s, 来源渠道=%s",
		fileHeader.Filename, float64(fileHeader.Size)/1024, targetJobID, sourceChannel)

	// 3. 打开文件以供读取
	file, err := fileHeader.Open()
	if err != nil {
		log.Printf("打开上传文件失败: %v", err)
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "处理上传文件失败"})
		return
	}
	defer file.Close()

	// 4. 调用业务逻辑处理器处理简历上传
	resp, err := h.resumeHandler.HandleResumeUpload(
		ctx, // 使用Hertz的上下文
		file,
		fileHeader.Size,
		fileHeader.Filename,
		targetJobID,
		sourceChannel,
	)

	if err != nil {
		log.Printf("处理简历上传失败: %v", err)
		// 根据错误类型返回不同的HTTP状态码可能更好
		c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("处理简历上传时发生内部错误: %v", err)})
		return
	}

	// 5. 返回成功响应
	log.Printf("简历上传成功: SubmissionUUID=%s", resp.SubmissionUUID)
	c.JSON(consts.StatusOK, resp)
}
