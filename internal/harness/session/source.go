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
	SpillLocator string `json:"spill_locator,omitempty"`
	ID           string `json:"source_id"`
	Seq          int64  `json:"seq"`
	Type         string `json:"type"`
	Role         string `json:"role"`
	Interrupted  bool   `json:"interrupted"`
	Text         string `json:"text"`
	Offset       int    `json:"offset"`
	Next         int    `json:"next"`
	Total        int    `json:"total"`
	HasMore      bool   `json:"has_more"`
}

// SourceAt is a trusted storage discovery API, not a model-facing tool. The
// owner must come from the authorized adapter, never from a model argument.
func (RedisStore) SourceAt(ctx context.Context, owner uint, seq int64, offset, limit int) (SourcePage, error) {
	return readSource(ctx, owner, "", seq, offset, limit)
}

func (RedisStore) ReadSource(ctx context.Context, owner uint, id string, offset, limit int) (SourcePage, error) {
	gen, seq, err := parseSource(owner, id)
	if err != nil {
		return SourcePage{}, err
	}
	return readSource(ctx, owner, gen, seq, offset, limit)
}

func parseSource(owner uint, id string) (string, int64, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 5 || parts[0] != "history" || parts[1] != "v1" || parts[2] != strconv.FormatUint(uint64(owner), 10) {
		return "", 0, ErrSourceUnavailable
	}
	b, err := hex.DecodeString(parts[3])
	if err != nil || len(b) != 16 {
		return "", 0, ErrSourceUnavailable
	}
	seq, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || seq < 0 {
		return "", 0, ErrSourceUnavailable
	}
	return parts[3], seq, nil
}

var neighborSourceScript = redis.NewScript(`
local gen = redis.call('GET', KEYS[2])
if not gen or gen ~= ARGV[1] then return {} end
if not redis.call('LINDEX', KEYS[1], ARGV[2]) then return {} end
return redis.call('LRANGE', KEYS[1], ARGV[3], ARGV[4])
`)

// SourceNeighbors returns adjacent raw events (including the anchor), not
// projected messages. At most 11 previews of 200 code points each. It does not
// skip audit/tool events or expand spill pointers; callers can page each ID.
func (RedisStore) SourceNeighbors(ctx context.Context, owner uint, id string, before, after int) ([]SourcePage, error) {
	gen, seq, err := parseSource(owner, id)
	if err != nil {
		return nil, err
	}
	if before < 0 || after < 0 || before > 5 || after > 5 {
		return nil, errors.New("neighbor bounds must be 0..5")
	}
	if rdb == nil {
		return nil, errors.New("redis client is nil")
	}
	start := max(int64(0), seq-int64(before))
	// Saturate instead of overflowing an untrusted seq near MaxInt64.
	end := seq
	if seq <= int64(^uint64(0)>>1)-int64(after) {
		end += int64(after)
	}
	values, err := neighborSourceScript.Run(ctx, rdb, sourceKeys(owner), gen, seq, start, end).StringSlice()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrSourceUnavailable
	}
	pages := make([]SourcePage, 0, len(values))
	for i, raw := range values {
		p, err := sourcePage(owner, gen, start+int64(i), raw, 0, 200)
		if err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return pages, nil
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
	return sourcePage(owner, values[0], seq, values[1], offset, limit)
}

func sourcePage(owner uint, gen string, seq int64, raw string, offset, limit int) (SourcePage, error) {
	var dto MemoryDTO
	if err := json.Unmarshal([]byte(raw), &dto); err != nil {
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
	return SourcePage{ID: fmt.Sprintf("history:v1:%d:%s:%d", owner, gen, seq), Seq: seq,
		SpillLocator: dto.SpillLocator,
		Type:         dto.Type, Role: dto.Role, Interrupted: dto.Interrupted, Text: string(text[offset:end]),
		Offset: offset, Next: end, Total: len(text), HasMore: end < len(text)}, nil
}
