package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/redis/go-redis/v9"
	"go_im_gateway/internal/harness/runstate"
)

// ChildEventStore keeps child steps out of the parent's projected history.
type ChildEventStore interface {
	AppendChildEvents(context.Context, uint, []MemoryDTO) error
	GetChildLog(context.Context, uint, string) ([]MemoryDTO, error)
	ChildRunIDs(context.Context, uint, string) ([]string, error)
}

func childKey(uid uint, id string) string { return fmt.Sprintf("agent:child:history:%d:%s", uid, id) }
func childrenKey(uid uint, parent string) string {
	return fmt.Sprintf("agent:child:index:%d:%s", uid, parent)
}

func (RedisStore) AppendChildEvents(ctx context.Context, uid uint, batch []MemoryDTO) error {
	ref, err := runstate.Bind(ctx, uid, nil)
	if err != nil {
		return err
	}
	if ref == nil || ref.ParentRunID == "" || ref.ParentToolCallID == "" || ref.ParentRunID == ref.RunID {
		return fmt.Errorf("child log requires independent run and parent call")
	}
	if rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	if len(batch) == 0 {
		return nil
	}
	values := make([]any, 0, len(batch))
	for _, dto := range batch {
		dto.Run, err = runstate.Bind(ctx, uid, dto.Run)
		if err != nil {
			return err
		}
		if err := prepareForWrite(&dto); err != nil {
			return err
		}
		data, err := json.Marshal(dto)
		if err != nil {
			return err
		}
		values = append(values, data)
	}
	_, err = rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.RPush(ctx, childKey(uid, ref.RunID), values...)
		pipe.SAdd(ctx, childrenKey(uid, ref.ParentRunID), ref.RunID)
		return nil
	})
	return err
}

func (RedisStore) GetChildLog(ctx context.Context, uid uint, id string) ([]MemoryDTO, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	rows, err := rdb.LRange(ctx, childKey(uid, id), 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]MemoryDTO, 0, len(rows))
	for _, row := range rows {
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(row), &dto); err != nil {
			return nil, err
		}
		if dto.V > LogFormatVersion || dto.Run == nil || dto.Run.UserID != uid || dto.Run.RunID != id {
			return nil, fmt.Errorf("invalid child log identity or version")
		}
		out = append(out, dto)
	}
	return out, nil
}

func (RedisStore) ChildRunIDs(ctx context.Context, uid uint, parent string) ([]string, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	ids, err := rdb.SMembers(ctx, childrenKey(uid, parent)).Result()
	sort.Strings(ids)
	return ids, err
}
