package outbox // 定义了发件箱模式（Outbox Pattern）的实现

import (
	"ai-agent-go/internal/storage"        // 导入项目内部的存储包，用于访问消息队列等
	"ai-agent-go/internal/storage/models" // 导入数据库模型
	"context"                             // 导入上下文包
	"log"                                 // 导入标准日志库，用于发件箱服务的内部日志
	"time"                                // 导入时间包

	"gorm.io/gorm"        // 导入GORM库
	"gorm.io/gorm/clause" // 导入GORM的子句构建器，如此处的锁

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultPollingInterval = 5 * time.Second // 默认轮询数据库中 outbox 表的间隔
	defaultBatchSize       = 10              // 每次轮询处理的消息批量大小
	maxRetryCount          = 5               // 消息发布失败的最大重试次数
)

// MessageRelay 轮询 outbox 表并将消息发布到消息代理。
type MessageRelay struct {
	db              *gorm.DB          // GORM数据库连接实例
	publisher       *storage.RabbitMQ // 使用现有的RabbitMQ客户端作为消息发布器
	logger          *log.Logger       // 用于记录中继服务自身日志的记录器
	pollingInterval time.Duration     // 轮询间隔
	batchSize       int               // 批量大小
	done            chan struct{}     // 用于优雅地停止服务的通道
	tracer          trace.Tracer      // OpenTelemetry追踪器
}

// NewMessageRelay 创建一个新的 MessageRelay 实例。
func NewMessageRelay(db *gorm.DB, publisher *storage.RabbitMQ, logger *log.Logger) *MessageRelay {
	return &MessageRelay{
		db:              db,                          // 初始化数据库连接
		publisher:       publisher,                   // 初始化消息发布器
		logger:          logger,                      // 初始化日志记录器
		pollingInterval: defaultPollingInterval,      // 设置默认轮询间隔
		batchSize:       defaultBatchSize,            // 设置默认批量大小
		done:            make(chan struct{}),         // 创建用于停止信号的通道
		tracer:          otel.Tracer("outbox-relay"), // 初始化追踪器
	}
}

// Start 开始消息中继的轮询过程。
func (r *MessageRelay) Start() {
	r.logger.Println("MessageRelay starting...") // 记录服务启动日志
	ticker := time.NewTicker(r.pollingInterval)  // 创建一个按指定间隔触发的定时器

	go func() { // 启动一个后台goroutine来执行轮询
		for {
			select {
			case <-r.done: // 如果接收到停止信号
				ticker.Stop()                             // 停止定时器
				r.logger.Println("MessageRelay stopped.") // 记录服务停止日志
				return                                    // 退出goroutine
			case <-ticker.C: // 当定时器触发时
				if err := r.processPendingMessages(context.Background()); err != nil { // 处理待处理的消息
					r.logger.Printf("Error processing pending messages: %v", err) // 如果处理失败，记录错误日志
				}
			}
		}
	}()
}

// Stop 优雅地停止消息中继服务。
func (r *MessageRelay) Stop() {
	r.logger.Println("MessageRelay stopping...") // 记录服务正在停止的日志
	close(r.done)                                // 关闭done通道，向后台goroutine发送停止信号
}

// processPendingMessages 获取并处理一批来自 outbox 表的待处理消息。
func (r *MessageRelay) processPendingMessages(ctx context.Context) error {
	var messages []models.OutboxMessage // 用于存储从数据库获取的消息

	// 启动一个数据库事务，以确保获取和更新消息的原子性。
	// 注意：这里的查询没有包含在追踪Span内，这是故意的，以避免为空轮询创建Span。
	tx := r.db.WithContext(ctx).Begin()
	if tx.Error != nil { // 如果开启事务失败
		return tx.Error
	}
	defer tx.Rollback() // 延迟执行回滚。如果事务被提交，回滚是无操作的。

	// 获取一批待处理的消息，并使用数据库锁来防止其他实例处理相同的消息。
	// `FOR UPDATE SKIP LOCKED` 对于水平扩展至关重要，它会跳过已被其他事务锁定的行。
	err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}). // 添加行级锁
											Where("status = ?", "PENDING"). // 查询状态为 "PENDING" 的消息
											Order("created_at asc").        // 按创建时间升序排序，确保先进先出
											Limit(r.batchSize).             // 限制获取的数量
											Find(&messages).Error           // 执行查询

	if err != nil { // 如果查询失败
		r.logger.Printf("Failed to fetch pending outbox messages: %v", err)
		return err
	}

	// 只有在找到要处理的消息时，才开始追踪
	if len(messages) == 0 {
		return tx.Commit().Error // 如果没有消息，直接提交空事务并返回
	}

	// 仅在有消息时创建追踪Span
	ctx, span := r.tracer.Start(ctx, "outbox.ProcessBatch",
		trace.WithAttributes(
			attribute.Int("messaging.batch.message_count", len(messages)),
		),
	)
	defer span.End()

	r.logger.Printf("Fetched %d pending messages to process.", len(messages))

	for _, msg := range messages { // 遍历获取到的消息
		// 调用发布器将消息发送到消息队列
		err := r.publisher.PublishMessage(
			ctx,                  // 上下文
			msg.TargetExchange,   // 目标交换机
			msg.TargetRoutingKey, // 目标路由键
			[]byte(msg.Payload),  // 消息体
			true,                 // 设置消息为持久化
		)

		if err != nil { // 如果发布失败
			r.logger.Printf("Failed to publish message ID %d (AggregateID: %s): %v. Retries: %d", msg.ID, msg.AggregateID, err, msg.RetryCount+1)
			msg.RetryCount++                     // 增加重试次数
			msg.ErrorMessage = err.Error()       // 记录错误信息
			if msg.RetryCount >= maxRetryCount { // 如果达到最大重试次数
				msg.Status = "FAILED" // 将状态标记为 "FAILED"
			}
		} else { // 如果发布成功
			msg.Status = "SENT"    // 将状态标记为 "SENT"
			now := time.Now()      // 获取当前时间
			msg.ProcessedAt = &now // 记录处理时间
			msg.ErrorMessage = ""  // 清空错误信息
		}

		// 在事务中更新数据库中的消息状态
		if err := tx.Save(&msg).Error; err != nil {
			r.logger.Printf("Failed to update outbox message ID %d: %v", msg.ID, err)
			// 如果更新失败，整个事务将回滚。
			// 这条消息的状态不会被改变，它将在下一次轮询中被重新拾取。
			return err
		}
	}

	return tx.Commit().Error // 提交事务
}
