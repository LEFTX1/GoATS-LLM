
```markdown
agent-go/
│
├── agent/                  # 之前的demo项目，包含阿里云LLM客户端
│
├── bin/                    # 编译后的二进制文件存放目录
│
├── cmd/                    # 命令行工具
│   └── resumeprocessor/    # 简历处理命令行工具
│       ├── chunk_cmd.go    # 简历分块命令处理
│       ├── embed.go        # 向量嵌入处理命令
│       ├── extract_cmd.go  # 文本提取命令处理
│       └── main.go         # 命令行入口程序
│
├── pkg/                    # 核心包
│   ├── config/             # 配置管理
│   │   └── config.go       # 配置加载和处理
│   │
│   ├── parser/             # 解析和处理相关
│   │   ├── embedding.go    # 向量嵌入接口和基础实现
│   │   ├── llm_chunker.go  # 基于LLM的简历分块器
│   │   ├── pdf_parser.go   # PDF文档解析器
│   │   └── resume_chunker.go # 规则式简历分块器
│   │
│   ├── processor/          # 处理流程和业务逻辑
│   │   ├── adapters.go     # 各种组件的适配器
│   │   ├── factory.go      # 处理器工厂和构建器
│   │   ├── interfaces.go   # 核心接口定义
│   │   └── resume_processor.go # 简历处理主逻辑
│   │
│   ├── storage/            # 存储相关
│   │   ├── mysql_adapter.go # MySQL数据库适配器
│   │   ├── qdrant_adapter.go # Qdrant向量库适配器
│   │   └── qdrant_client.go # Qdrant HTTP客户端
│   │
│   └── types/              # 共享数据类型
│       └── resume_types.go # 简历相关数据结构定义
│
├── testdata/              # 测试数据文件
│
├── config.sample.yaml     # 配置样例文件
├── config.yaml           # 运行时配置文件
├── go.mod               # Go模块定义
├── go.sum               # 依赖版本控制
├── main.go              # 应用程序主入口
└── README.md            # 项目说明文档
```
