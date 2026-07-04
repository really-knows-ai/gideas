package queue

import (
	"context"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// Service provides queue operations across all shards in a namespace.
type Service interface {
	ListQueues(ctx context.Context) ([]string, error)
	GetQueueItems(ctx context.Context, queueName string) ([]*flowv1.QueueItem, error)
}
