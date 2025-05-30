package singleton_test

import (
	"strings"
	"testing"

	"ai-agent-go/pkg/storage/models"
	"ai-agent-go/pkg/storage/singleton"
)

// TestMySQLConnection 测试MySQL的连接和自动迁移
func TestMySQLConnection(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetMySQLAdapter()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	t.Logf("Config path found by FindTestConfigFile: %s", configPath)
	testCfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}

	// 检查配置是否有效
	if testCfg.MySQL.Host == "" || testCfg.MySQL.Username == "" || testCfg.MySQL.Database == "" {
		t.Fatalf("MySQL配置无效，请确保config.yaml中包含有效的MySQL配置")
	}

	//
	t.Logf("%v", testCfg.MySQL)

	// 获取MySQL适配器实例
	adapter, err := singleton.GetMySQLAdapter(testCfg.MySQL)
	if err != nil {
		// 检查是否是连接错误
		if strings.Contains(err.Error(), "Access denied") ||
			strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "Cannot connect") {
			t.Skipf("无法连接到MySQL服务器，请检查配置: %v", err)
		}
		t.Fatalf("获取MySQL适配器失败: %v", err)
	}

	// 测试数据库连接
	db := adapter.DB()
	if db == nil {
		t.Fatal("无法获取数据库连接")
	}

	// 验证连接是否正常工作 - 尝试Ping
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层sql.DB失败: %v", err)
	}

	// 尝试Ping，如果失败则跳过测试
	if err := sqlDB.Ping(); err != nil {
		t.Skipf("Ping数据库连接失败，跳过测试: %v", err)
	}

	t.Log("成功连接到MySQL数据库")

	// 测试表是否已正确迁移 - 检查所有模型对应的表是否存在
	// 这里我们只检查一部分表作为示例
	tablesToCheck := []string{
		"candidates",
		"jobs",
		"resume_submissions",
		"resume_submission_chunks",
		"job_submission_matches",
	}

	for _, table := range tablesToCheck {
		var exists bool
		// 通过执行SQL查询检查表是否存在
		query := "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = ? AND table_name = ?)"
		err := db.Raw(query, testCfg.MySQL.Database, table).Scan(&exists).Error
		if err != nil {
			t.Errorf("检查表 %s 是否存在时出错: %v", table, err)
			continue
		}

		if !exists {
			t.Errorf("表 %s 应该存在，但未找到", table)
		} else {
			t.Logf("表 %s 存在", table)
		}
	}

	// 测试简单的CRUD操作 - 创建候选人
	testCandidate := &models.Candidate{
		CandidateID:  "test-uuid-1234",
		PrimaryName:  "测试候选人",
		PrimaryEmail: "test@example.com",
		PrimaryPhone: "1234567890",
		CreatedAt:    db.NowFunc(),
	}

	// 创建记录
	result := db.Create(testCandidate)
	if result.Error != nil {
		t.Fatalf("创建候选人记录失败: %v", result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("创建候选人记录受影响行数不为1: %d", result.RowsAffected)
	}
	if testCandidate.CandidateID == "" {
		t.Fatalf("创建的候选人记录ID无效: %s", testCandidate.CandidateID)
	}
	t.Logf("成功创建候选人记录，ID: %s", testCandidate.CandidateID)

	// 读取记录
	readCandidate := &models.Candidate{}
	if err := db.First(readCandidate, "candidate_id = ?", testCandidate.CandidateID).Error; err != nil {
		t.Fatalf("读取候选人记录失败: %v", err)
	}
	if readCandidate.PrimaryName != testCandidate.PrimaryName {
		t.Errorf("读取的候选人姓名不匹配: 期望 %s, 实际 %s", testCandidate.PrimaryName, readCandidate.PrimaryName)
	}
	t.Logf("成功读取候选人记录: %s", readCandidate.PrimaryName)

	// 删除测试记录 - 清理
	if err := db.Delete(testCandidate, "candidate_id = ?", testCandidate.CandidateID).Error; err != nil {
		t.Errorf("删除测试候选人记录失败: %v", err)
	} else {
		t.Log("成功删除测试候选人记录")
	}

	t.Log("MySQL操作测试成功完成")
}
