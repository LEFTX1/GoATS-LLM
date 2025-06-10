package handler_test

import (
	"ai-agent-go/internal/api/handler"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/types"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route/param"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	paginatedTestConfigPath = "../../config/config.yaml"
)

// TestResumePaginatedHandler 测试简历分页查询处理器
func TestResumePaginatedHandler(t *testing.T) {
	// 跳过CI环境测试
	if testing.Short() {
		t.Skip("在短模式下跳过此测试")
	}

	// 1. 设置测试环境
	ctx := context.Background()

	// 加载配置
	cfg, err := config.LoadConfigFromFileOnly(paginatedTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")

	// 2. 初始化存储组件
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	// 检查Redis是否可用
	if s.Redis == nil {
		t.Skip("Redis未配置或不可用，跳过测试")
	}

	// 3. 可以测试两种情况：
	// A. 使用TestFullSearchWithRedisCache生成的真实数据
	// B. 生成测试数据

	// 选择测试方式
	useRealData := true // 设置为true使用真实数据，false生成测试数据
	testJobID := ""

	if useRealData == true {
		// A. 使用TestFullSearchWithRedisCache生成的真实数据
		testJobID = "11111111-2222-3333-4444-555555555555" // 与TestFullSearchWithRedisCache使用的相同ID
		t.Logf("使用真实数据进行测试，JobID: %s", testJobID)
	} else {
		// B. 生成测试数据
		testJobID = "test-job-id-for-pagination"
		// HR ID 在这个测试中不是必需的，因为我们在后面使用固定值

		cacheKey := fmt.Sprintf("search_session:%s", testJobID)

		t.Logf("使用生成的测试数据，JobID: %s, 缓存键: %s", testJobID, cacheKey)

		// 清除旧的缓存数据
		_, err = s.Redis.Client.Del(ctx, cacheKey).Result()
		if err != nil && err != redis.Nil {
			t.Logf("清除缓存时出错: %v", err)
		}

		// 创建测试数据 - 将10个UUID存入Redis
		testUUIDs := []string{
			"11111111-1111-1111-1111-111111111111",
			"22222222-2222-2222-2222-222222222222",
			"33333333-3333-3333-3333-333333333333",
			"44444444-4444-4444-4444-444444444444",
			"55555555-5555-5555-5555-555555555555",
			"66666666-6666-6666-6666-666666666666",
			"77777777-7777-7777-7777-777777777777",
			"88888888-8888-8888-8888-888888888888",
			"99999999-9999-9999-9999-999999999999",
			"00000000-0000-0000-0000-000000000000",
		}

		// 按照ZREVRANGE的排序要求，将分数设置为逆序排列（最高分在最前）
		for i, uuid := range testUUIDs {
			// 使用10-i作为分数，确保按降序排列
			err = s.Redis.Client.ZAdd(ctx, cacheKey, redis.Z{
				Score:  float64(10 - i), // 让分数递减，模拟排序
				Member: uuid,
			}).Err()
			require.NoError(t, err, "向Redis添加测试数据失败")
		}
	}

	// 4. 初始化处理器
	h := handler.NewResumePaginatedHandler(cfg, s)

	// 5. 创建请求上下文
	c := app.NewContext(16)

	// 添加URL参数
	c.Params = append(c.Params, param.Param{
		Key:   "job_id",
		Value: testJobID,
	})

	// 设置HR ID到上下文中，模拟认证中间件的行为
	c.Set("hr_id", "test-hr-id") // 对于简单测试，任何非空值都可以

	// 添加查询参数 - 获取前5个结果
	c.QueryArgs().Add("cursor", "0")
	c.QueryArgs().Add("size", "5")

	// 6. 执行测试
	h.HandlePaginatedResumeSearch(ctx, c)

	// 7. 验证结果
	statusCode := c.Response.StatusCode()
	assert.Equal(t, consts.StatusOK, statusCode)

	var respData types.PaginatedResumeResponse
	err = json.Unmarshal(c.Response.Body(), &respData)
	require.NoError(t, err, "解析响应失败")

	// 8. 输出并验证结果
	t.Logf("查询结果: JobID=%s, 总数=%d, 返回%d个简历",
		respData.JobID, respData.TotalCount, len(respData.Resumes))

	// 验证基本数据
	assert.Equal(t, testJobID, respData.JobID)
	assert.Equal(t, int64(0), respData.Cursor)
	assert.True(t, respData.TotalCount >= 0, "总数应该大于等于0")

	// 显示简历信息
	for i, resume := range respData.Resumes {
		t.Logf("简历 #%d: UUID=%s, 块数=%d",
			i+1, resume.SubmissionUUID, len(resume.Chunks))
	}

	// 9. 获取下一页
	if respData.NextCursor != respData.Cursor && respData.TotalCount > 0 {
		t.Logf("测试获取下一页，游标=%d", respData.NextCursor)

		c2 := app.NewContext(16)
		c2.Params = append(c2.Params, param.Param{
			Key:   "job_id",
			Value: testJobID,
		})
		c2.Set("hr_id", "test-hr-id")
		c2.QueryArgs().Add("cursor", fmt.Sprintf("%d", respData.NextCursor))
		c2.QueryArgs().Add("size", "5")

		h.HandlePaginatedResumeSearch(ctx, c2)

		statusCode = c2.Response.StatusCode()
		assert.Equal(t, consts.StatusOK, statusCode)

		var respData2 types.PaginatedResumeResponse
		err = json.Unmarshal(c2.Response.Body(), &respData2)
		require.NoError(t, err, "解析第二页响应失败")

		t.Logf("第二页查询结果: 游标=%d, 下一页游标=%d, 返回%d个简历",
			respData2.Cursor, respData2.NextCursor, len(respData2.Resumes))

		// 显示第二页简历信息
		for i, resume := range respData2.Resumes {
			t.Logf("第二页简历 #%d: UUID=%s, 块数=%d",
				i+1, resume.SubmissionUUID, len(resume.Chunks))
		}
	}

	// 10. 如果是使用生成的测试数据，清理
	if false {
		cacheKey := fmt.Sprintf("search_session:%s", testJobID)
		_, err = s.Redis.Client.Del(ctx, cacheKey).Result()
		if err != nil && err != redis.Nil {
			t.Logf("清理缓存时出错: %v", err)
		}
	}
}
