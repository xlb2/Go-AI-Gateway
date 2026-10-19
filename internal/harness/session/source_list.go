package session

import (
	"context"
	"errors"
	"strconv"

	"github.com/redis/go-redis/v9"
)

type SourceList struct {
	Sources []SourcePage `json:"sources"`
	Next    string       `json:"next_cursor,omitempty"`
	HasMore bool         `json:"has_more"`
}

// A cursor is the oldest source on the preceding page; subsequent reads are
// strictly before it. New appends cannot shift pages or cause duplicates.
var listSourceScript = redis.NewScript(`
local total = redis.call('LLEN', KEYS[1])
if total == 0 then return {} end
redis.call('SETNX', KEYS[2], ARGV[1])
local gen = redis.call('GET', KEYS[2])
local last = total - 1
if ARGV[2] ~= '' then
  if gen ~= ARGV[2] or not redis.call('LINDEX', KEYS[1], ARGV[3]) then return {} end
  last = tonumber(ARGV[3]) - 1
end
if last < 0 then return {gen, '0'} end
local first = math.max(0, last - tonumber(ARGV[4]) + 1)
local rows = redis.call('LRANGE', KEYS[1], first, last)
local result = {gen, tostring(first)}
for i = 1, #rows do result[#result+1] = rows[i] end
return result
`)

// ListSources previews raw events newest first, including non-message events.
// Returned text is capped; Redis still reads each full persisted event.
func (RedisStore) ListSources(ctx context.Context, owner uint, cursor string, limit int) (SourceList, error) {
	result := SourceList{Sources: []SourcePage{}}
	if limit < 1 || limit > 5 {
		return result, errors.New("source list limit must be 1..5")
	}
	if rdb == nil {
		return result, errors.New("redis client is nil")
	}
	expected, seq := "", int64(0)
	var err error
	if cursor != "" {
		expected, seq, err = parseSource(owner, cursor)
		if err != nil {
			return result, err
		}
	}
	gen, err := newGeneration()
	if err != nil {
		return result, err
	}
	values, err := listSourceScript.Run(ctx, rdb, sourceKeys(owner), gen, expected, seq, limit).StringSlice()
	if err != nil {
		return result, err
	}
	if len(values) == 0 {
		if cursor != "" {
			return result, ErrSourceUnavailable
		}
		return result, nil
	}
	start, err := strconv.ParseInt(values[1], 10, 64)
	if err != nil {
		return result, err
	}
	for i := len(values) - 1; i >= 2; i-- {
		page, err := sourcePage(owner, values[0], start+int64(i-2), values[i], 0, 200)
		if err != nil {
			return SourceList{}, err
		}
		result.Sources = append(result.Sources, page)
	}
	if start > 0 && len(result.Sources) > 0 {
		result.HasMore = true
		result.Next = result.Sources[len(result.Sources)-1].ID
	}
	return result, nil
}
