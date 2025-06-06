package outbox

import (
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"context"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultPollingInterval = 5 * time.Second
	defaultBatchSize       = 10
	maxRetryCount          = 5
)

// MessageRelay polls the outbox table and publishes messages to a message broker.
type MessageRelay struct {
	db              *gorm.DB
	publisher       *storage.RabbitMQ // Using the existing RabbitMQ client as the publisher
	logger          *log.Logger
	pollingInterval time.Duration
	batchSize       int
	done            chan struct{}
}

// NewMessageRelay creates a new MessageRelay instance.
func NewMessageRelay(db *gorm.DB, publisher *storage.RabbitMQ, logger *log.Logger) *MessageRelay {
	return &MessageRelay{
		db:              db,
		publisher:       publisher,
		logger:          logger,
		pollingInterval: defaultPollingInterval,
		batchSize:       defaultBatchSize,
		done:            make(chan struct{}),
	}
}

// Start begins the message relay polling process.
func (r *MessageRelay) Start() {
	r.logger.Println("MessageRelay starting...")
	ticker := time.NewTicker(r.pollingInterval)

	go func() {
		for {
			select {
			case <-r.done:
				ticker.Stop()
				r.logger.Println("MessageRelay stopped.")
				return
			case <-ticker.C:
				if err := r.processPendingMessages(context.Background()); err != nil {
					r.logger.Printf("Error processing pending messages: %v", err)
				}
			}
		}
	}()
}

// Stop gracefully stops the message relay.
func (r *MessageRelay) Stop() {
	r.logger.Println("MessageRelay stopping...")
	close(r.done)
}

// processPendingMessages fetches and processes a batch of pending messages from the outbox.
func (r *MessageRelay) processPendingMessages(ctx context.Context) error {
	var messages []models.OutboxMessage

	// Transaction to ensure atomicity of fetching and updating messages.
	tx := r.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback() // Rollback is a no-op if the transaction is committed.

	// Fetch a batch of pending messages, locking them to prevent other instances from processing them.
	// `FOR UPDATE SKIP LOCKED` is crucial for horizontal scalability.
	err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
		Where("status = ?", "PENDING").
		Order("created_at asc").
		Limit(r.batchSize).
		Find(&messages).Error

	if err != nil {
		r.logger.Printf("Failed to fetch pending outbox messages: %v", err)
		return err
	}

	if len(messages) > 0 {
		r.logger.Printf("Fetched %d pending messages to process.", len(messages))
	}

	for _, msg := range messages {
		err := r.publisher.PublishMessage(
			ctx,
			msg.TargetExchange,
			msg.TargetRoutingKey,
			[]byte(msg.Payload),
			true, // persistent
		)

		if err != nil {
			r.logger.Printf("Failed to publish message ID %d (AggregateID: %s): %v. Retries: %d", msg.ID, msg.AggregateID, err, msg.RetryCount+1)
			msg.RetryCount++
			msg.ErrorMessage = err.Error()
			if msg.RetryCount >= maxRetryCount {
				msg.Status = "FAILED"
			}
		} else {
			msg.Status = "SENT"
			now := time.Now()
			msg.ProcessedAt = &now
			msg.ErrorMessage = ""
		}

		// Update the message status in the database
		if err := tx.Save(&msg).Error; err != nil {
			r.logger.Printf("Failed to update outbox message ID %d: %v", msg.ID, err)
			// The transaction will be rolled back, so this update won't be committed anyway.
			// The message will be picked up again in the next poll.
			return err
		}
	}

	return tx.Commit().Error
}
