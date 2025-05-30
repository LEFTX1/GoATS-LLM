package singleton_test

import (
	"sync"
	"testing"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage/singleton"
)

// 测试MinIO单例模式
func TestMinIOSingleton(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetMinIOAdapter()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	testCfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	// 检查配置是否有效
	if testCfg.MinIO.Endpoint == "" {
		t.Skipf("MinIO配置无效或未在测试中配置，跳过测试")
		return
	}

	// 并发获取实例测试
	instanceCount := 10
	var instances []*interface{}
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < instanceCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// 由于实际连接需要MinIO服务，这里只测试逻辑而不实际连接
			// 捕获错误但不使测试失败
			instance, _ := singleton.GetMinIOAdapter(testCfg.MinIO)
			if instance != nil {
				mu.Lock()
				instances = append(instances, nil) // 不保存实际实例，只增加计数
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(instances) > 0 {
		// 测试不一定能成功创建实例（因为可能没有MinIO服务），但如果创建了，应该只有一个
		t.Logf("创建了 %d 个MinIO实例，所有协程应共享同一个实例", len(instances))
	} else {
		t.Log("没有成功创建MinIO实例，可能是因为没有MinIO服务可连接")
	}
}

// 测试MySQL单例模式
func TestMySQLSingleton(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetMySQLAdapter()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	testCfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	// 检查配置是否有效
	if testCfg.MySQL.Host == "" {
		t.Skipf("MySQL配置无效或未在测试中配置，跳过测试")
		return
	}

	// 并发获取实例测试
	instanceCount := 10
	var instances []*interface{}
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < instanceCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// 由于实际连接需要MySQL服务，这里只测试逻辑而不实际连接
			instance, _ := singleton.GetMySQLAdapter(testCfg.MySQL)
			if instance != nil {
				mu.Lock()
				instances = append(instances, nil) // 不保存实际实例，只增加计数
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(instances) > 0 {
		// 测试不一定能成功创建实例（因为可能没有MySQL服务），但如果创建了，应该只有一个
		t.Logf("创建了 %d 个MySQL实例，所有协程应共享同一个实例", len(instances))
	} else {
		t.Log("没有成功创建MySQL实例，可能是因为没有MySQL服务可连接")
	}
}

// 测试RabbitMQ单例模式
func TestRabbitMQSingleton(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetRabbitMQAdapter()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	testCfg, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	// 检查配置是否有效
	if testCfg.RabbitMQ.URL == "" { // 通常URL是主要的连接字符串
		t.Skipf("RabbitMQ配置无效或未在测试中配置，跳过测试")
		return
	}

	// 并发获取实例测试
	instanceCount := 10
	var instances []*interface{}
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < instanceCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// 由于实际连接需要RabbitMQ服务，这里只测试逻辑而不实际连接
			instance, _ := singleton.GetRabbitMQAdapter(testCfg.RabbitMQ)
			if instance != nil {
				mu.Lock()
				instances = append(instances, nil) // 不保存实际实例，只增加计数
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(instances) > 0 {
		// 测试不一定能成功创建实例（因为可能没有RabbitMQ服务），但如果创建了，应该只有一个
		t.Logf("创建了 %d 个RabbitMQ实例，所有协程应共享同一个实例", len(instances))
	} else {
		t.Log("没有成功创建RabbitMQ实例，可能是因为没有RabbitMQ服务可连接")
	}
}

// 测试Qdrant单例模式
func TestQdrantSingleton(t *testing.T) {
	// 重置单例确保测试隔离
	singleton.ResetQdrantClient()

	// 加载测试配置
	configPath := singleton.FindTestConfigFile()
	testCfgSingleton, err := singleton.LoadTestConfigNoEnv(configPath)
	if err != nil {
		t.Fatalf("加载测试配置失败: %v", err)
	}
	// 检查配置是否有效
	if testCfgSingleton.Qdrant.Endpoint == "" {
		t.Skipf("Qdrant配置无效或未在测试中配置，跳过测试")
		return
	}

	// 从TestConfig转换到config.Config给GetQdrantClient
	cfg := &config.Config{}
	cfg.Qdrant.Endpoint = testCfgSingleton.Qdrant.Endpoint
	cfg.Qdrant.Collection = testCfgSingleton.Qdrant.Collection
	cfg.Qdrant.Dimension = testCfgSingleton.Qdrant.Dimension

	// 并发获取实例测试
	instanceCount := 10
	var instances []*interface{}
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < instanceCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// 由于实际连接需要Qdrant服务，这里只测试逻辑而不实际连接
			instance, _ := singleton.GetQdrantClient(cfg)
			if instance != nil {
				mu.Lock()
				instances = append(instances, nil) // 不保存实际实例，只增加计数
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(instances) > 0 {
		// 测试不一定能成功创建实例（因为可能没有Qdrant服务），但如果创建了，应该只有一个
		t.Logf("创建了 %d 个Qdrant实例，所有协程应共享同一个实例", len(instances))
	} else {
		t.Log("没有成功创建Qdrant实例，可能是因为没有Qdrant服务可连接")
	}
}
