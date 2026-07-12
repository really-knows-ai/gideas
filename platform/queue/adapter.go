package queue

import (
	"context"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/queue/internal/rest"
)

type handlerAdapter struct {
	h *rest.Handler
}

func (a *handlerAdapter) ListQueues(ctx context.Context) ([]string, error) {
	return a.h.ListQueues(ctx)
}

func (a *handlerAdapter) GetQueueItems(ctx context.Context, queueName string) ([]*flowv1.QueueItem, error) {
	return a.h.GetQueueItems(ctx, queueName)
}

// NewService wraps a rest.Handler as a Service.
func NewService(h *rest.Handler) Service {
	return &handlerAdapter{h: h}
}
