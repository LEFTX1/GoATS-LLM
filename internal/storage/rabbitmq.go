package storage

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/tracing"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// 全局变量和辅助类型
var rabbitmqTracer = otel.Tracer("ai-agent-go/storage/rabbitmq")

// AmqpHeadersCarrier 实现了 propagation.TextMapCarrier 接口
type AmqpHeadersCarrier amqp.Table

// Get retrieves a value from the carrier
func (a AmqpHeadersCarrier) Get(key string) string {
	if value, exists := a[key]; exists {
		// 处理不同类型的值
		switch v := value.(type) {
		case string:
			return v
		case []byte:
			return string(v)
		case int, int32, int64, float32, float64:
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

// Set stores a value in the carrier
func (a AmqpHeadersCarrier) Set(key, value string) {
	// 将键名统一转为小写，确保与Keys()方法保持一致
	a[strings.ToLower(key)] = value
}

// Keys returns all keys in the carrier
func (a AmqpHeadersCarrier) Keys() []string {
	keys := make([]string, 0, len(a))
	for k := range a {
		// RabbitMQ header中键名大小写不敏感，统一转为小写
		keys = append(keys, strings.ToLower(k))
	}
	return keys
}

// MessageQueue 消息队列接口
type MessageQueue interface {
	// PublishMessage 发布消息
	PublishMessage(ctx context.Context, exchangeName, routingKey string, message []byte, persistent bool) error

	// PublishJSON 发布JSON格式消息
	PublishJSON(ctx context.Context, exchangeName, routingKey string, data interface{}, persistent bool) error

	// EnsureExchange 确保交换机存在
	EnsureExchange(exchangeName, exchangeType string, durable bool) error

	// EnsureQueue 确保队列存在
	EnsureQueue(queueName string, durable bool) error

	// BindQueue 绑定队列到交换机
	BindQueue(queueName, exchangeName, routingKey string) error

	// StartConsumer 启动消费者处理单条消息
	StartConsumer(queueName string, prefetchCount int, handler func([]byte) bool) (<-chan struct{}, error)

	// StartBatchConsumer 启动消费者处理批量消息
	StartBatchConsumer(queueName string, batchSize int, batchTimeout time.Duration, prefetchCount int, handler func([]amqp.Delivery) bool) (<-chan struct{}, error)

	// Close 关闭连接
	Close() error
}

// 确保RabbitMQ实现了MessageQueue接口
var _ MessageQueue = (*RabbitMQ)(nil)

// RabbitMQ 提供消息队列功能
type RabbitMQ struct {
	conn           *amqp.Connection
	channelPool    sync.Pool
	exchangeMap    map[string]bool // 记录已声明的exchange
	queueMap       map[string]bool // 记录已声明的queue
	bindingMap     map[string]bool // 记录已创建的binding (key格式: "exchange:queue:routingKey")
	mapMutex       sync.RWMutex    // 保护map访问的互斥锁
	publishMutex   sync.Mutex      // 保护发布操作
	cfg            *config.RabbitMQConfig
	confirmChannel *amqp.Channel
	channelMutex   sync.RWMutex
}

// NewRabbitMQ 创建RabbitMQ客户端
func NewRabbitMQ(cfg *config.RabbitMQConfig) (*RabbitMQ, error) {
	if cfg == nil {
		return nil, fmt.Errorf("RabbitMQ配置不能为空")
	}

	// 设置默认的确认超时时间
	if cfg.ConfirmTimeout <= 0 {
		cfg.ConfirmTimeout = 5 * time.Second // 默认5秒
	}

	// 使用配置中的完整URL
	if cfg.URL == "" {
		return nil, fmt.Errorf("RabbitMQ URL配置不能为空")
	}

	// 建立连接
	conn, err := amqp.Dial(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("无法连接到RabbitMQ服务器 (%s): %w", cfg.URL, err)
	}

	mq := &RabbitMQ{
		conn:        conn,
		exchangeMap: make(map[string]bool),
		queueMap:    make(map[string]bool),
		bindingMap:  make(map[string]bool),
		cfg:         cfg,
	}

	// 初始化channel池
	mq.channelPool = sync.Pool{
		New: func() interface{} {
			ch, errPool := conn.Channel() // Renamed err to errPool to avoid conflict
			if errPool != nil {
				log.Printf("创建RabbitMQ通道失败: %v", errPool)
				return nil
			}
			return ch
		},
	}

	// 测试连接和通道
	testCh := mq.getChannel()
	if testCh == nil {
		conn.Close()
		return nil, fmt.Errorf("无法创建RabbitMQ通道")
	}
	mq.putChannel(testCh)

	log.Printf("成功连接到RabbitMQ服务器: %s", cfg.URL)
	return mq, nil
}

// 获取可用通道
func (r *RabbitMQ) getChannel() *amqp.Channel {
	ch := r.channelPool.Get()
	if ch == nil {
		newCh, err := r.conn.Channel()
		if err != nil {
			log.Printf("创建新RabbitMQ通道失败: %v", err)
			return nil
		}
		return newCh
	}
	return ch.(*amqp.Channel)
}

// 归还通道到池
func (r *RabbitMQ) putChannel(ch *amqp.Channel) {
	if ch != nil {
		r.channelPool.Put(ch)
	}
}

// Close 关闭连接
func (r *RabbitMQ) Close() error {
	return r.conn.Close()
}

// EnsureExchange 确保exchange存在
func (r *RabbitMQ) EnsureExchange(exchangeName, exchangeType string, durable bool) error {
	// 添加安全检查，防止交换机名为空
	if exchangeName == "" {
		return fmt.Errorf("exchange名称不能为空")
	}

	// 防止尝试声明默认交换机
	if exchangeName == "amq.default" || exchangeName == "default" {
		return fmt.Errorf("不能声明默认交换机 '%s'", exchangeName)
	}

	log.Printf("准备声明交换机: '%s', 类型: '%s', 持久化: %v", exchangeName, exchangeType, durable)

	// 使用读锁检查exchange是否已经存在
	r.mapMutex.RLock()
	exists := r.exchangeMap[exchangeName]
	r.mapMutex.RUnlock()

	if exists {
		log.Printf("交换机 '%s' 已经存在于本地缓存中，跳过声明", exchangeName)
		return nil
	}

	ch := r.getChannel()
	if ch == nil {
		return fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer r.putChannel(ch)

	log.Printf("正在声明交换机: '%s'", exchangeName)
	err := ch.ExchangeDeclare(
		exchangeName, // exchange名称
		exchangeType, // exchange类型
		durable,      // 持久化
		false,        // 自动删除
		false,        // 内部专用
		false,        // 非阻塞
		nil,          // 参数
	)
	if err != nil {
		return fmt.Errorf("声明exchange失败: %w", err)
	}

	// 使用写锁更新exchangeMap
	r.mapMutex.Lock()
	r.exchangeMap[exchangeName] = true
	r.mapMutex.Unlock()

	log.Printf("已确保exchange存在: '%s'", exchangeName)
	return nil
}

// EnsureQueue 确保队列存在
func (r *RabbitMQ) EnsureQueue(queueName string, durable bool) error {
	// 使用读锁检查队列是否已存在于缓存中
	r.mapMutex.RLock()
	exists := r.queueMap[queueName]
	r.mapMutex.RUnlock()

	if exists {
		// 队列已在本地缓存中标记为存在，使用 QueueDeclarePassive 模式来获取队列信息并验证
		ch := r.getChannel()
		if ch == nil {
			return fmt.Errorf("无法获取RabbitMQ通道")
		}
		defer r.putChannel(ch)

		// 使用 QueueDeclarePassive 替代 QueueInspect
		// Passive 声明模式下，如果队列存在且参数匹配，则返回队列信息。
		// 如果队列不存在，或者参数不匹配，则返回错误。
		_, err := ch.QueueDeclarePassive(
			queueName, // name
			durable,   // durable
			false,     // delete when unused (设置为false以匹配大多数QueueDeclare的常见用法)
			false,     // exclusive (设置为false以匹配大多数QueueDeclare的常见用法)
			false,     // no-wait
			nil,       // arguments
		)
		if err != nil {
			// 如果被动声明失败，可能意味着队列实际不存在或参数不匹配。
			// 从本地缓存中移除，以便下次尝试重新声明。
			r.mapMutex.Lock()
			delete(r.queueMap, queueName)
			r.mapMutex.Unlock()
			return fmt.Errorf("被动声明队列 '%s' 失败 (可能不存在或参数不匹配): %w", queueName, err)
		}
		return nil
	}

	ch := r.getChannel()
	if ch == nil {
		return fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer r.putChannel(ch)

	_, err := ch.QueueDeclare(
		queueName, // 队列名称
		durable,   // 持久化
		false,     // 自动删除
		false,     // 独占
		false,     // 非阻塞
		nil,       // 参数
	)
	if err != nil {
		return fmt.Errorf("声明队列失败: %w", err)
	}

	// 使用写锁更新queueMap
	r.mapMutex.Lock()
	r.queueMap[queueName] = true
	r.mapMutex.Unlock()

	log.Printf("已确保队列存在: %s", queueName)
	return nil
}

// BindQueue 绑定队列到exchange
func (r *RabbitMQ) BindQueue(queueName, exchangeName, routingKey string) error {
	bindingKey := fmt.Sprintf("%s:%s:%s", exchangeName, queueName, routingKey)

	// 使用读锁检查binding是否已存在
	r.mapMutex.RLock()
	exists := r.bindingMap[bindingKey]
	r.mapMutex.RUnlock()

	if exists {
		return nil
	}

	ch := r.getChannel()
	if ch == nil {
		return fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer r.putChannel(ch)

	err := ch.QueueBind(
		queueName,    // 队列名
		routingKey,   // 路由键
		exchangeName, // exchange名
		false,        // 非阻塞
		nil,          // 参数
	)
	if err != nil {
		return fmt.Errorf("绑定队列到exchange失败: %w", err)
	}

	// 使用写锁更新bindingMap
	r.mapMutex.Lock()
	r.bindingMap[bindingKey] = true
	r.mapMutex.Unlock()

	log.Printf("已绑定队列 %s 到exchange %s，路由键: %s", queueName, exchangeName, routingKey)
	return nil
}

// PublishMessage 发布消息到exchange
func (r *RabbitMQ) PublishMessage(ctx context.Context, exchangeName, routingKey string, message []byte, persistent bool) error {
	// 创建发布span
	ctx, span := rabbitmqTracer.Start(ctx, "rabbitmq.publish",
		trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()

	// 设置span属性
	span.SetAttributes(
		attribute.String("messaging.system", "rabbitmq"),
		attribute.String("messaging.destination_kind", "exchange"),
		attribute.String("messaging.destination", exchangeName),
		attribute.String("messaging.routing_key", routingKey),
		attribute.String("messaging.protocol", "AMQP"),
		attribute.String("messaging.protocol_version", "0.9.1"),
		attribute.Int("messaging.message_payload_size_bytes", len(message)),
	)

	r.publishMutex.Lock()
	defer r.publishMutex.Unlock()

	// 获取通道
	ch := r.getChannel()
	if ch == nil {
		err := fmt.Errorf("无法获取RabbitMQ通道")
		tracing.RecordError(span, err, tracing.ErrorTypeRabbitMQ)
		return err
	}
	// 获取通道后立即添加defer，确保任何错误情况下都会归还通道到池中
	defer r.putChannel(ch)

	// 启用发布确认模式
	if err := ch.Confirm(false); err != nil {
		tracing.RecordError(span, err, tracing.ErrorTypeRabbitMQ)
		return err
	}

	// 设置消息属性
	var deliveryMode uint8 = 1 // 非持久化
	if persistent {
		deliveryMode = 2 // 持久化
	}

	// 生成消息ID用于跟踪
	messageID := uuid.New().String()
	span.SetAttributes(attribute.String("messaging.message_id", messageID))

	// 提取追踪上下文，注入到消息头部
	headers := amqp.Table{}
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(ctx, AmqpHeadersCarrier(headers))

	// 获取确认通道
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 1))

	// 使用context控制超时
	err := ch.PublishWithContext(
		ctx,
		exchangeName, // exchange名
		routingKey,   // 路由键
		false,        // 强制
		false,        // 立即
		amqp.Publishing{
			DeliveryMode: deliveryMode,
			ContentType:  "application/json",
			Body:         message,
			Timestamp:    time.Now(),
			Headers:      headers, // 添加头部，包含追踪上下文
			MessageId:    messageID,
		},
	)

	if err != nil {
		tracing.RecordError(span, err, tracing.ErrorTypeRabbitMQ)
		return err
	}

	// 创建confirm子span来专门跟踪确认结果
	_, confirmSpan := rabbitmqTracer.Start(ctx, "publisher.confirm",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.operation", "confirm"),
			attribute.String("messaging.message_id", messageID),
		),
	)

	// 设置确认超时，优先使用配置的超时时间，否则使用默认值
	confirmTimeout := 5 * time.Second // 默认值
	if r.cfg != nil && r.cfg.ConfirmTimeout > 0 {
		confirmTimeout = r.cfg.ConfirmTimeout
	}
	confirmSpan.SetAttributes(attribute.Int64("messaging.confirm_timeout_ms", confirmTimeout.Milliseconds()))

	select {
	case confirm := <-confirms:
		if confirm.Ack {
			confirmSpan.SetAttributes(attribute.Bool("messaging.rabbitmq.confirmed", true))
			confirmSpan.SetStatus(codes.Ok, "message confirmed")
		} else {
			tracing.RecordRabbitMQNack(confirmSpan, messageID, "")
		}
	case <-time.After(confirmTimeout):
		tracing.RecordRabbitMQTimeout(confirmSpan, messageID, confirmTimeout.String())
	}
	confirmSpan.End()

	span.SetStatus(codes.Ok, "")
	return nil
}

// PublishJSON 发布JSON格式的消息
func (r *RabbitMQ) PublishJSON(ctx context.Context, exchangeName, routingKey string, data interface{}, persistent bool) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	return r.PublishMessage(ctx, exchangeName, routingKey, jsonData, persistent)
}

// StartConsumer 启动消费者处理单条消息
func (r *RabbitMQ) StartConsumer(queueName string, prefetchCount int, handler func([]byte) bool) (<-chan struct{}, error) {
	stopCh := make(chan struct{})

	// 为每个消费者创建独立通道而不是从池中获取，避免共享通道导致的Qos设置冲突
	ch, err := r.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("无法创建RabbitMQ通道: %w", err)
	}

	// 设置QoS，控制预取数量
	if err := ch.Qos(prefetchCount, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("设置QoS失败: %w", err)
	}

	// 获取消息通道
	deliveries, err := ch.Consume(
		queueName, // 队列
		"",        // 消费者标签，留空由server生成唯一标签
		false,     // 自动确认
		false,     // 独占
		false,     // 非本地
		false,     // 非阻塞
		nil,       // 参数
	)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("注册消费者失败: %w", err)
	}

	// 启动消费者协程
	go func() {
		defer ch.Close() // 直接关闭通道，不放回池中
		defer log.Printf("RabbitMQ消费者已停止: %s", queueName)

		log.Printf("RabbitMQ消费者已启动，队列: %s, 预取数量: %d", queueName, prefetchCount)

		for {
			select {
			case <-stopCh:
				return
			case delivery, ok := <-deliveries:
				if !ok {
					log.Println("RabbitMQ通道已关闭")
					return
				}

				// 从消息头部提取追踪上下文
				ctx := extractContextFromDelivery(delivery)

				// 创建消费者span
				ctx, span := rabbitmqTracer.Start(ctx, fmt.Sprintf("ConsumeMessage from %s", queueName),
					trace.WithSpanKind(trace.SpanKindConsumer))

				// 设置span属性
				span.SetAttributes(
					attribute.String("messaging.system", "rabbitmq"),
					attribute.String("messaging.destination", queueName),
					attribute.String("messaging.message_id", delivery.MessageId),
					attribute.Int("messaging.message_payload_size_bytes", len(delivery.Body)),
					attribute.String("messaging.rabbitmq.routing_key", delivery.RoutingKey),
				)

				// 在处理消息前记录开始处理的事件
				span.AddEvent("message_processing_started")

				// 调用处理函数
				success := handler(delivery.Body)

				if success {
					if err := delivery.Ack(false); err != nil {
						log.Printf("确认消息失败: %v", err)
						span.RecordError(err)
						span.SetStatus(codes.Error, "ack failed")
					} else {
						span.SetStatus(codes.Ok, "processed successfully")
					}
				} else {
					// 处理失败，拒绝并重新入队
					if err := delivery.Nack(false, true); err != nil {
						log.Printf("拒绝消息失败: %v", err)
						span.RecordError(err)
						span.SetStatus(codes.Error, "nack failed")
					} else {
						span.SetStatus(codes.Error, "message processing failed")
					}
				}

				// 记录处理结束事件
				span.AddEvent("message_processing_completed", trace.WithAttributes(
					attribute.Bool("success", success),
				))

				// 结束span
				span.End()
			}
		}
	}()

	return stopCh, nil
}

// StartBatchConsumer 启动一个消费者，该消费者会批量处理消息。
// 它会收集消息直到达到 batchSize 或 batchTimeout，然后将整个批次传递给处理函数。
// 处理函数负责确认或拒绝整个批次。
func (r *RabbitMQ) StartBatchConsumer(queueName string, batchSize int, batchTimeout time.Duration, prefetchCount int, handler func([]amqp.Delivery) bool) (<-chan struct{}, error) {
	// 为批量消费者创建独立通道
	ch, err := r.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("无法创建独立RabbitMQ通道: %w", err)
	}

	if err := ch.Qos(prefetchCount, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("设置QoS失败: %w", err)
	}

	msgs, err := ch.Consume(
		queueName,
		"",    // consumer
		false, // auto-ack
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,   // args
	)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("注册消费者失败: %w", err)
	}

	stopCh := make(chan struct{})

	go func() {
		defer ch.Close() // 直接关闭通道，不放回池中
		defer close(stopCh)

		batch := make([]amqp.Delivery, 0, batchSize)
		var timeout <-chan time.Time

		processBatch := func() {
			if len(batch) == 0 {
				return
			}

			// 创建一个基准上下文
			// 优先从第一条消息中提取上下文，如果没有则使用背景上下文
			ctx := extractContextFromDelivery(batch[0])

			// 创建一个批处理span
			ctx, span := rabbitmqTracer.Start(ctx, fmt.Sprintf("BatchConsume from %s", queueName),
				trace.WithSpanKind(trace.SpanKindConsumer))

			// 设置span属性
			span.SetAttributes(
				attribute.String("messaging.system", "rabbitmq"),
				attribute.String("messaging.destination", queueName),
				attribute.Int("messaging.batch.message_count", len(batch)),
			)

			// 记录处理开始的事件
			span.AddEvent("batch_processing_started")

			// 处理批次
			success := handler(batch)

			if success {
				// 确认整个批次
				if err := ch.Ack(batch[len(batch)-1].DeliveryTag, true); err != nil {
					log.Printf("批量ACK消息失败: %v", err)
					span.RecordError(err)
					span.SetStatus(codes.Error, "batch ack failed")
				} else {
					span.SetStatus(codes.Ok, "batch processed successfully")
				}
			} else {
				// 拒绝整个批次并重新入队
				if err := ch.Nack(batch[len(batch)-1].DeliveryTag, true, true); err != nil {
					log.Printf("批量NACK消息失败: %v", err)
					span.RecordError(err)
					span.SetStatus(codes.Error, "batch nack failed")
				} else {
					span.SetStatus(codes.Error, "batch processing failed")
				}
			}

			// 记录处理结束事件
			span.AddEvent("batch_processing_completed", trace.WithAttributes(
				attribute.Bool("success", success),
			))

			// 结束span
			span.End()

			// 清空批次
			batch = make([]amqp.Delivery, 0, batchSize)
			timeout = nil
		}

		for {
			select {
			case d, ok := <-msgs:
				if !ok {
					log.Println("RabbitMQ channel 已关闭，正在退出批量消费者...")
					processBatch() // 处理剩余的消息
					return
				}
				batch = append(batch, d)
				if len(batch) == 1 {
					timeout = time.After(batchTimeout)
				}
				if len(batch) >= batchSize {
					processBatch()
				}
			case <-timeout:
				processBatch()
			}
		}
	}()

	return stopCh, nil
}

// 辅助函数：从AMQP消息中提取追踪上下文
func extractContextFromDelivery(delivery amqp.Delivery) context.Context {
	// 创建背景上下文
	ctx := context.Background()

	// 如果消息中没有头部，直接返回背景上下文
	if delivery.Headers == nil {
		return ctx
	}

	// 使用自定义载体实现
	carrier := AmqpHeadersCarrier(delivery.Headers)

	// 从载体中提取上下文
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
