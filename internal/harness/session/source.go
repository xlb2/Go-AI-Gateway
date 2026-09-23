package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

var ErrSourceUnavailable = errors.New("history source unavailable or stale")

func sourceKeys(owner uint) []string {
	return []string{fmt.Sprintf("agent:V2:history:%d", owner), fmt.Sprintf("agent:V2:generation:%d", owner)}
}

func newGeneration() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Both append paths use this script. Empty/recreated logs always receive a new
// generation, even if an old generation key survived deletion of the log.
var appendSourceScript = redis.NewScript(`
local n = redis.call('LLEN', KEYS[1])
if n == 0 then
  redis.call('SET', KEYS[2], ARGV[1])
else
  redis.call('SETNX', KEYS[2], ARGV[1])
end
return redis.call('RPUSH', KEYS[1], unpack(ARGV, 2))
`)

func appendSourceEvents(ctx context.Context, owner uint, values ...any) error {
	gen, err := newGeneration()
	if err != nil {
		return err
	}
	args := append([]any{gen}, values...)
	return appendSourceScript.Run(ctx, rdb, sourceKeys(owner), args...).Err()
}

// Bootstrap legacy logs without rewriting their events. Generation validation
// and LINDEX are atomic, so a stale reference never reads a replacement log.
var readSourceScript = redis.NewScript(`
if redis.call('LLEN', KEYS[1]) == 0 then return {} end
redis.call('SETNX', KEYS[2], ARGV[1])
local gen = redis.call('GET', KEYS[2])
if ARGV[2] ~= '' and ARGV[2] ~= gen then return {} end
local event = redis.call('LINDEX', KEYS[1], ARGV[3])
if not event then return {} end
return {gen, event}
`)

// SourcePage contains only persisted Content, not expanded spill text or tool
// arguments. Offsets count Unicode code points. Event type/status stay visible.
type SourcePage struct {
	ID          string `json:"source_id"`
	Seq         int64  `json:"seq"`
	Type        string `json:"type"`
	Role        string `json:"role"`
	Interrupted bool   `json:"interrupted"`
	Text        string `json:"text"`
	Offset      int    `json:"offset"`
	Next        int    `json:"next"`
	Total       int    `json:"total"`
	HasMore     bool   `json:"has_more"`
}

// SourceAt is a trusted storage discovery API, not a model-facing tool. The
// owner must come from the authorized adapter, never from a model argument.
func (RedisStore) SourceAt(ctx context.Context, owner uint, seq int64, offset, limit int) (SourcePage, error) {
	return readSource(ctx, owner, "", seq, offset, limit)
}

func (RedisStore) ReadSource(ctx context.Context, owner uint, id string, offset, limit int) (SourcePage, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 5 || parts[0] != "history" || parts[1] != "v1" || parts[2] != strconv.FormatUint(uint64(owner), 10) {
		return SourcePage{}, ErrSourceUnavailable
	}
	b, err := hex.DecodeString(parts[3])
	if err != nil || len(b) != 16 {
		return SourcePage{}, ErrSourceUnavailable
	}
	seq, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || seq < 0 {
		return SourcePage{}, ErrSourceUnavailable
	}
	return readSource(ctx, owner, parts[3], seq, offset, limit)
}

func readSource(ctx context.Context, owner uint, expected string, seq int64, offset, limit int) (SourcePage, error) {
	if seq < 0 || offset < 0 || limit < 1 || limit > 2000 {
		return SourcePage{}, errors.New("invalid source page: seq/offset >= 0, limit 1..2000")
	}
	if rdb == nil {
		return SourcePage{}, errors.New("redis client is nil")
	}
	gen, err := newGeneration()
	if err != nil {
		return SourcePage{}, err
	}
	values, err := readSourceScript.Run(ctx, rdb, sourceKeys(owner), gen, expected, seq).StringSlice()
	if err != nil {
		return SourcePage{}, err
	}
	if len(values) != 2 {
		return SourcePage{}, ErrSourceUnavailable
	}
	var dto MemoryDTO
	if err := json.Unmarshal([]byte(values[1]), &dto); err != nil {
		return SourcePage{}, fmt.Errorf("corrupt source: %w", err)
	}
	if dto.V > LogFormatVersion {
		return SourcePage{}, errors.New("unsupported source log version")
	}
	text := []rune(dto.Content)
	if offset > len(text) {
		return SourcePage{}, errors.New("source offset beyond content")
	}
	end := offset + min(limit, len(text)-offset)
	return SourcePage{ID: fmt.Sprintf("history:v1:%d:%s:%d", owner, values[0], seq), Seq: seq,
		Type: dto.Type, Role: dto.Role, Interrupted: dto.Interrupted, Text: string(text[offset:end]),
		Offset: offset, Next: end, Total: len(text), HasMore: end < len(text)}, nil
}
