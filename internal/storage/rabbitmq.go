package storage

import (
	"ai-agent-go/internal/config"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// MessageQueue 消息队列接口
type MessageQueue interface {
	// 发布消息
	PublishMessage(ctx context.Context, exchangeName, routingKey string, message []byte, persistent bool) error

	// 发布JSON格式消息
	PublishJSON(ctx context.Context, exchangeName, routingKey string, data interface{}, persistent bool) error

	// 确保交换机存在
	EnsureExchange(exchangeName, exchangeType string, durable bool) error

	// 确保队列存在
	EnsureQueue(queueName string, durable bool) error

	// 绑定队列到交换机
	BindQueue(queueName, exchangeName, routingKey string) error

	// 关闭连接
	Close() error
}

// 确保RabbitMQ实现了MessageQueue接口
var _ MessageQueue = (*RabbitMQ)(nil)

// RabbitMQ 提供消息队列功能
type RabbitMQ struct {
	conn         *amqp.Connection
	channelPool  sync.Pool
	exchangeMap  map[string]bool // 记录已声明的exchange
	queueMap     map[string]bool // 记录已声明的queue
	bindingMap   map[string]bool // 记录已创建的binding (key格式: "exchange:queue:routingKey")
	publishMutex sync.Mutex      // 保护发布操作
	cfg          *config.RabbitMQConfig
}

// NewRabbitMQ 创建RabbitMQ客户端
func NewRabbitMQ(cfg *config.RabbitMQConfig) (*RabbitMQ, error) {
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

	if _, exists := r.exchangeMap[exchangeName]; exists {
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

	r.exchangeMap[exchangeName] = true
	log.Printf("已确保exchange存在: '%s'", exchangeName)
	return nil
}

// EnsureQueue 确保队列存在
func (r *RabbitMQ) EnsureQueue(queueName string, durable bool) error {
	if _, exists := r.queueMap[queueName]; exists {
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
			delete(r.queueMap, queueName)
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

	r.queueMap[queueName] = true
	log.Printf("已确保队列存在: %s", queueName)
	return nil
}

// BindQueue 绑定队列到exchange
func (r *RabbitMQ) BindQueue(queueName, exchangeName, routingKey string) error {
	bindingKey := fmt.Sprintf("%s:%s:%s", exchangeName, queueName, routingKey)
	if _, exists := r.bindingMap[bindingKey]; exists {
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

	r.bindingMap[bindingKey] = true
	log.Printf("已绑定队列 %s 到exchange %s，路由键: %s", queueName, exchangeName, routingKey)
	return nil
}

// PublishMessage 发布消息到exchange
func (r *RabbitMQ) PublishMessage(ctx context.Context, exchangeName, routingKey string, message []byte, persistent bool) error {
	r.publishMutex.Lock()
	defer r.publishMutex.Unlock()

	ch := r.getChannel()
	if ch == nil {
		return fmt.Errorf("无法获取RabbitMQ通道")
	}
	defer r.putChannel(ch)

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
func (r *RabbitMQ) PublishJSON(ctx context.Context, exchangeName, routingKey string, data interface{}, persistent bool) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %w", err)
	}

	return r.PublishMessage(ctx, exchangeName, routingKey, jsonData, persistent)
}

// StartConsumer 启动消费者处理函数
func (r *RabbitMQ) StartConsumer(queueName string, prefetchCount int, handler func([]byte) bool) (<-chan struct{}, error) {
	stopCh := make(chan struct{})

	ch := r.getChannel()
	if ch == nil {
		return nil, fmt.Errorf("无法获取RabbitMQ通道")
	}

	// 设置QoS，控制预取数量
	if err := ch.Qos(prefetchCount, 0, false); err != nil {
		r.putChannel(ch)
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
		r.putChannel(ch)
		return nil, fmt.Errorf("注册消费者失败: %w", err)
	}

	// 启动消费者协程
	go func() {
		defer r.putChannel(ch)
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
