package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"ai-agent-go/pkg/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQAdapter 提供RabbitMQ消息队列的封装
type RabbitMQAdapter struct {
	conn         *amqp.Connection
	channelPool  sync.Pool
	exchangeMap  map[string]bool // 记录已声明的exchange
	queueMap     map[string]bool // 记录已声明的queue
	bindingMap   map[string]bool // 记录已创建的binding (key格式: "exchange:queue:routingKey")
	publishMutex sync.Mutex      // 保护发布操作
	cfg          *config.RabbitMQConfig
}

// NewRabbitMQAdapter 创建新的RabbitMQ适配器
func NewRabbitMQAdapter(cfg *config.RabbitMQConfig) (*RabbitMQAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("RabbitMQ配置不能为空")
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

	adapter := &RabbitMQAdapter{
		conn:        conn,
		exchangeMap: make(map[string]bool),
		queueMap:    make(map[string]bool),
		bindingMap:  make(map[string]bool),
		cfg:         cfg,
	}

	// 初始化channel池
	adapter.channelPool = sync.Pool{
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
	testCh := adapter.getChannel()
	if testCh == nil {
		conn.Close()
		return nil, fmt.Errorf("无法创建RabbitMQ通道")
	}
	adapter.putChannel(testCh)

	log.Printf("成功连接到RabbitMQ服务器: %s", cfg.URL)
	return adapter, nil
}

// 获取可用通道
func (a *RabbitMQAdapter) getChannel() *amqp.Channel {
	ch := a.channelPool.Get()
	if ch == nil {
		newCh, err := a.conn.Channel()
		if err != nil {
			log.Printf("创建新RabbitMQ通道失败: %v", err)
			return nil
		}
		return newCh
	}
	return ch.(*amqp.Channel)
}

// 归还通道到池
func (a *RabbitMQAdapter) putChannel(ch *amqp.Channel) {
	if ch != nil {
		a.channelPool.Put(ch)
	}
}

// Close 关闭连接
func (a *RabbitMQAdapter) Close() error {
	return a.conn.Close()
}

// EnsureExchange 确保exchange存在
func (a *RabbitMQAdapter) EnsureExchange(exchangeName, exchangeType string, durable bool) error {
	// 添加安全检查，防止交换机名为空
	if exchangeName == "" {
		return fmt.Errorf("exchange名称不能为空")
	}

	// 防止尝试声明默认交换机
	if exchangeName == "amq.default" || exchangeName == "default" {
		return fmt.Errorf("不能声明默认交换机 '%s'", exchangeName)
	}

	log.Printf("准备声明交换机: '%s', 类型: '%s', 持久化: %v", exchangeName, exchangeType, durable)

	if _, exists := a.exchangeMap[exchangeName]; exists {
		log.Printf("交换机 '%s' 已经存在于本地缓存中，跳过声明", exchangeName)
		return nil
	}

	ch := a.getChannel()
	if ch == nil {
		return fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer a.putChannel(ch)

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

	a.exchangeMap[exchangeName] = true
	log.Printf("已确保exchange存在: '%s'", exchangeName)
	return nil
}

// EnsureQueue 确保队列存在
func (a *RabbitMQAdapter) EnsureQueue(queueName string, durable bool) (amqp.Queue, error) {
	if _, exists := a.queueMap[queueName]; exists {
		// 队列已存在，返回队列对象
		ch := a.getChannel()
		if ch == nil {
			return amqp.Queue{}, fmt.Errorf("无法获取RabbitMQ通道")
		}
		defer a.putChannel(ch)

		queue, err := ch.QueueInspect(queueName)
		return queue, err
	}

	ch := a.getChannel()
	if ch == nil {
		return amqp.Queue{}, fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer a.putChannel(ch)

	queue, err := ch.QueueDeclare(
		queueName, // 队列名称
		durable,   // 持久化
		false,     // 自动删除
		false,     // 独占
		false,     // 非阻塞
		nil,       // 参数
	)
	if err != nil {
		return amqp.Queue{}, fmt.Errorf("声明队列失败: %w", err)
	}

	a.queueMap[queueName] = true
	log.Printf("已确保队列存在: %s", queueName)
	return queue, nil
}

// BindQueue 绑定队列到exchange
func (a *RabbitMQAdapter) BindQueue(queueName, exchangeName, routingKey string) error {
	bindingKey := fmt.Sprintf("%s:%s:%s", exchangeName, queueName, routingKey)
	if _, exists := a.bindingMap[bindingKey]; exists {
		return nil
	}

	ch := a.getChannel()
	if ch == nil {
		return fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer a.putChannel(ch)

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

	a.bindingMap[bindingKey] = true
	log.Printf("已绑定队列 %s 到exchange %s，路由键: %s", queueName, exchangeName, routingKey)
	return nil
}

// PublishMessage 发布消息到exchange
func (a *RabbitMQAdapter) PublishMessage(ctx context.Context, exchangeName, routingKey string, message []byte, persistent bool) error {
	a.publishMutex.Lock()
	defer a.publishMutex.Unlock()

	ch := a.getChannel()
	if ch == nil {
		return fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer a.putChannel(ch)

	// 设置消息属性
	var deliveryMode uint8 = 1 // 非持久化
	if persistent {
		deliveryMode = 2 // 持久化
	}

	// 使用context控制超时
	return ch.PublishWithContext(
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
		},
	)
}

// PublishJSON 发布JSON格式的消息
func (a *RabbitMQAdapter) PublishJSON(ctx context.Context, exchangeName, routingKey string, data interface{}, persistent bool) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	return a.PublishMessage(ctx, exchangeName, routingKey, jsonData, persistent)
}

// ResumeUploadMessage 定义简历上传消息结构
type ResumeUploadMessage struct {
	SubmissionUUID        string    `json:"submission_uuid"`
	OriginalFileObjectKey string    `json:"original_file_object_key"`
	OriginalFilename      string    `json:"original_filename"`
	TargetJobID           string    `json:"target_job_id,omitempty"`
	SourceChannel         string    `json:"source_channel,omitempty"`
	ReceivedTimestamp     time.Time `json:"received_timestamp"`
}

// ResumeProcessingMessage 定义简历处理消息结构
type ResumeProcessingMessage struct {
	SubmissionUUID      string `json:"submission_uuid"`
	ParsedTextObjectKey string `json:"parsed_text_object_key,omitempty"`
	ParsedText          string `json:"parsed_text,omitempty"` // 可选，直接包含解析后的文本
	TargetJobID         string `json:"target_job_id,omitempty"`
}

// StartConsumer 启动消费者处理函数
func (a *RabbitMQAdapter) StartConsumer(queueName string, prefetchCount int, handler func([]byte) bool) (<-chan struct{}, error) {
	stopCh := make(chan struct{})

	ch := a.getChannel()
	if ch == nil {
		return nil, fmt.Errorf("无法获取RabbitMQ通道")
	}

	// 设置QoS，控制预取数量
	if err := ch.Qos(prefetchCount, 0, false); err != nil {
		a.putChannel(ch)
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
		a.putChannel(ch)
		return nil, fmt.Errorf("注册消费者失败: %w", err)
	}

	// 启动消费者协程
	go func() {
		defer a.putChannel(ch)
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

				// 调用处理函数
				if handler(delivery.Body) {
					if err := delivery.Ack(false); err != nil {
						log.Printf("确认消息失败: %v", err)
					}
				} else {
					// 处理失败，拒绝并重新入队
					if err := delivery.Nack(false, true); err != nil {
						log.Printf("拒绝消息失败: %v", err)
					}
				}
			}
		}
	}()

	return stopCh, nil
}

// BatchConsumer 批量消费者结构
type BatchConsumer struct {
	adapter         *RabbitMQAdapter
	queueName       string
	channel         *amqp.Channel
	deliveries      <-chan amqp.Delivery
	batchSize       int
	batchTimeout    time.Duration
	processingBatch []*amqp.Delivery
	batchFull       chan bool
	batchTimer      *time.Timer
	handler         func([]*amqp.Delivery) ([]*amqp.Delivery, error)
	stopCh          chan struct{}
	stopped         bool
	mu              sync.Mutex
}

// StartBatchConsumer 创建并启动一个批量消费者
func (a *RabbitMQAdapter) StartBatchConsumer(
	queueName string,
	batchSize int,
	batchTimeout time.Duration,
	handler func([]*amqp.Delivery) ([]*amqp.Delivery, error),
) (*BatchConsumer, error) {
	ch := a.getChannel()
	if ch == nil {
		return nil, fmt.Errorf("无法获取RabbitMQ通道")
	}

	// 设置QoS
	if err := ch.Qos(batchSize*2, 0, false); err != nil {
		a.putChannel(ch)
		return nil, fmt.Errorf("设置QoS失败: %w", err)
	}

	deliveries, err := ch.Consume(
		queueName, // 队列
		"",        // 消费者标签
		false,     // 自动确认
		false,     // 独占
		false,     // 非本地
		false,     // 非阻塞
		nil,       // 参数
	)
	if err != nil {
		a.putChannel(ch)
		return nil, fmt.Errorf("注册消费者失败: %w", err)
	}

	consumer := &BatchConsumer{
		adapter:         a,
		queueName:       queueName,
		channel:         ch,
		deliveries:      deliveries,
		batchSize:       batchSize,
		batchTimeout:    batchTimeout,
		processingBatch: make([]*amqp.Delivery, 0, batchSize),
		batchFull:       make(chan bool, 1),
		handler:         handler,
		stopCh:          make(chan struct{}),
	}

	// 启动超时计时器
	consumer.batchTimer = time.NewTimer(batchTimeout)

	// 启动处理协程
	go consumer.processMessages()
	go consumer.processBatches()

	log.Printf("RabbitMQ批量消费者已启动，队列: %s, 批大小: %d, 超时: %v", queueName, batchSize, batchTimeout)
	return consumer, nil
}

// processMessages 收集消息形成批次
func (c *BatchConsumer) processMessages() {
	defer func() {
		if !c.stopped {
			c.Stop()
		}
	}()

	for {
		select {
		case <-c.stopCh:
			return
		case delivery, ok := <-c.deliveries:
			if !ok {
				log.Printf("队列 %s 的消息通道已关闭", c.queueName)
				return
			}

			c.mu.Lock()
			c.processingBatch = append(c.processingBatch, &delivery)
			currentSize := len(c.processingBatch)
			c.mu.Unlock()

			// 如果达到批处理大小，触发批处理
			if currentSize >= c.batchSize {
				select {
				case c.batchFull <- true:
				default:
					// 通道已满，忽略
				}
			}
		}
	}
}

// processBatches 处理批次
func (c *BatchConsumer) processBatches() {
	defer func() {
		if !c.stopped {
			c.Stop()
		}
	}()

	for {
		select {
		case <-c.stopCh:
			return
		case <-c.batchTimer.C:
			// 超时批处理
			c.processCurrentBatch()
			c.batchTimer.Reset(c.batchTimeout)
		case <-c.batchFull:
			// 批次已满批处理
			c.processCurrentBatch()
			c.batchTimer.Reset(c.batchTimeout)
		}
	}
}

// processCurrentBatch 处理当前批次
func (c *BatchConsumer) processCurrentBatch() {
	c.mu.Lock()
	if len(c.processingBatch) == 0 {
		c.mu.Unlock()
		return
	}

	// 获取当前批次并重置
	currentBatch := c.processingBatch
	c.processingBatch = make([]*amqp.Delivery, 0, c.batchSize)
	c.mu.Unlock()

	// 执行批处理
	failedDeliveries, err := c.handler(currentBatch)
	if err != nil {
		log.Printf("批处理失败: %v", err)
		// 将所有失败的消息放回批处理队列
		c.mu.Lock()
		c.processingBatch = append(failedDeliveries, c.processingBatch...)
		c.mu.Unlock()
		return
	}

	// 对成功处理的消息进行确认
	for _, delivery := range currentBatch {
		failed := false
		for _, failedDelivery := range failedDeliveries {
			if delivery.DeliveryTag == failedDelivery.DeliveryTag {
				failed = true
				break
			}
		}

		if !failed {
			if err := delivery.Ack(false); err != nil {
				log.Printf("确认消息失败: %v", err)
			}
		} else {
			// 重新入队失败的消息
			if err := delivery.Nack(false, true); err != nil {
				log.Printf("拒绝消息失败: %v", err)
			}
		}
	}
}

// Stop 停止消费者
func (c *BatchConsumer) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.stopped {
		close(c.stopCh)
		c.stopped = true
		c.adapter.putChannel(c.channel)
		log.Printf("RabbitMQ批量消费者已停止，队列: %s", c.queueName)
	}
}
