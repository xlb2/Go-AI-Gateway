package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type ExecutionState string

const (
	Claimed   ExecutionState = "claimed"
	Running   ExecutionState = "running"
	Succeeded ExecutionState = "succeeded"
	Unknown   ExecutionState = "unknown"
	Rejected  ExecutionState = "rejected"
)

// Execution records dispatch evidence, not model continuation or exactly-once effects.
// Records do not expire automatically: an unresolved effect must remain inspectable.
type Execution struct {
	Proposal  PendingAction  `json:"proposal"`
	State     ExecutionState `json:"state"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// ExecutionStore must create Claimed atomically with ClaimPending's consumption.
// A failed transition never grants permission to execute or retry.
type ExecutionStore interface {
	ClaimStore
	GetExecution(context.Context, uint, string) (*Execution, error)
	TransitionExecution(context.Context, uint, string, ExecutionState, ExecutionState) error
}

func executionKey(userID uint) string { return fmt.Sprintf("agent:approval:executions:%d", userID) }

func (RedisStore) GetExecution(ctx context.Context, userID uint, id string) (*Execution, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	raw, err := rdb.HGet(ctx, executionKey(userID), id).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record Execution
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

var transitionScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], ARGV[1]) ~= ARGV[2] then return 0 end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[3])
return 1
`)

func (RedisStore) TransitionExecution(ctx context.Context, userID uint, id string, from, to ExecutionState) error {
	valid := from == Claimed && (to == Running || to == Rejected) || from == Running && (to == Succeeded || to == Unknown)
	if !valid {
		return fmt.Errorf("invalid approval transition: %s -> %s", from, to)
	}
	if rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	raw, err := rdb.HGet(ctx, executionKey(userID), id).Bytes()
	if err != nil {
		return err
	}
	var record Execution
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	if record.State != from || record.Proposal.ID != id {
		return ErrChanged
	}
	record.State, record.UpdatedAt = to, time.Now().UTC()
	next, err := json.Marshal(record)
	if err != nil {
		return err
	}
	n, err := transitionScript.Run(ctx, rdb, []string{executionKey(userID)}, id, string(raw), string(next)).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrChanged
	}
	return nil
}
