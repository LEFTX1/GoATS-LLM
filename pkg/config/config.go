package config

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config 应用程序配置
type Config struct {
	Aliyun struct {
		APIKey string `yaml:"api_key"`
		APIURL string `yaml:"api_url"`
		Model  string `yaml:"model"`
	} `yaml:"aliyun"`

	Qdrant struct {
		Endpoint   string `yaml:"endpoint"`
		Collection string `yaml:"collection"`
		Dimension  int    `yaml:"dimension"`
	} `yaml:"qdrant"`
}

// LoadConfig 从文件加载配置
func LoadConfig(configPath string) (*Config, error) {
	// 如果未指定配置文件路径，则尝试在默认位置查找
	if configPath == "" {
		// 尝试在常见位置查找配置文件
		searchPaths := []string{
			"config.yaml",
			"./config.yaml",
			"../config.yaml",
			filepath.Join(os.Getenv("HOME"), ".resume-processor", "config.yaml"),
		}

		for _, path := range searchPaths {
			if _, err := os.Stat(path); err == nil {
				configPath = path
				break
			}
		}

		// 如果仍找不到配置文件，使用默认路径
		if configPath == "" {
			configPath = "config.yaml"
		}
	}

	// 检查文件是否存在
	_, err := os.Stat(configPath)
	if err != nil {
		return nil, fmt.Errorf("配置文件不存在: %s", configPath)
	}

	// 读取配置文件
	data, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 解析配置文件
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 从环境变量覆盖配置（如果存在）
	if envKey := os.Getenv("ALIYUN_API_KEY"); envKey != "" {
		config.Aliyun.APIKey = envKey
	}
	if envURL := os.Getenv("ALIYUN_API_URL"); envURL != "" {
		config.Aliyun.APIURL = envURL
	}
	if envModel := os.Getenv("ALIYUN_MODEL"); envModel != "" {
		config.Aliyun.Model = envModel
	}

	return &config, nil
}

// 创建一个配置样例文件
func CreateSampleConfig(filePath string) error {
	// 创建一个默认配置
	config := Config{}
	config.Aliyun.APIKey = "your_api_key_here"
	config.Aliyun.APIURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	config.Aliyun.Model = "qwen-turbo"
	config.Qdrant.Endpoint = "http://localhost:6333"
	config.Qdrant.Collection = "resumes"
	config.Qdrant.Dimension = 1024

	// 将配置序列化为YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	// 将配置写入文件
	err = ioutil.WriteFile(filePath, data, 0644)
	if err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}

	return nil
}
