package models

import "time"

// OutboxMessage represents a message to be published asynchronously.
type OutboxMessage struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	AggregateID      string     `gorm:"type:varchar(36);not null;index"`
	EventType        string     `gorm:"type:varchar(255);not null"`
	Payload          string     `gorm:"type:json;not null"` // Storing as string to handle JSON
	TargetExchange   string     `gorm:"type:varchar(255);not null"`
	TargetRoutingKey string     `gorm:"type:varchar(255);not null"`
	Status           string     `gorm:"type:varchar(20);default:'PENDING';not null;index:idx_outbox_status_created_at"`
	RetryCount       int        `gorm:"default:0"`
	CreatedAt        time.Time  `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);index:idx_outbox_status_created_at,sort:asc"`
	ProcessedAt      *time.Time `gorm:"type:datetime(6);null"`
	ErrorMessage     string     `gorm:"type:text"`
}

// TableName specifies the table name for the OutboxMessage model.
func (OutboxMessage) TableName() string {
	return "outbox_messages"
}
