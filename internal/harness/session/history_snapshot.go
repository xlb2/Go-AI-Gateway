package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
	"go_im_gateway/internal/harness/tokenmeter"
)

// Read generation and events in one Redis operation; never bind an old read to
// the generation of a replacement log. A fold hint is used only when a summary
// in the same snapshot proves coverage of the omitted prefix.
var historySnapshotScript = redis.NewScript(`
local total = redis.call('LLEN', KEYS[1])
if total == 0 then return {} end
redis.call('SETNX', KEYS[2], ARGV[1])
local base = tonumber(redis.call('GET', KEYS[3])) or 0
local rows = nil
if base > 0 and base < total and base == math.floor(base) then
  rows = redis.call('LRANGE', KEYS[1], base, -1)
  local covered = false
  for i = #rows, 1, -1 do
    local ok, event = pcall(cjson.decode, rows[i])
    if ok and type(event) == 'table' and event.type == 'compaction/summary' then
      local first = event.compaction_from or 0
      local last = event.compaction_to or 0
      local version = event.v or 0
      if first == 0 and type(last) == 'number' and last == math.floor(last)
        and last >= base - 1 and last < base + i - 1
        and type(version) == 'number' and version <= tonumber(ARGV[2]) then
        covered = true
        break
      end
    end
  end
  if not covered then base = 0; rows = nil end
else
  base = 0
end
if not rows then rows = redis.call('LRANGE', KEYS[1], 0, -1) end
return {redis.call('GET', KEYS[2]), base, rows}
`)

// HistorySource binds projected content to a persisted event. Summary text and
// interrupted prefixes are projections, not verbatim copies of that event.
type HistorySource struct {
	ProjectedSource
	ID string
}

// GetHistorySources is an opt-in snapshot API, not the default GetHistory path.
// maxTokens uses the existing hard-window trim semantics (<=0 means no trim),
// not the shadow selector's mandatory-current-turn policy. Owner is trusted.
func (RedisStore) GetHistorySources(ctx context.Context, owner uint, maxTokens int) ([]HistorySource, error) {
	if rdb == nil {
		return nil, errors.New("redis client is nil")
	}
	gen, err := newGeneration()
	if err != nil {
		return nil, err
	}
	keys := append(sourceKeys(owner), foldKey(owner))
	values, err := historySnapshotScript.Run(ctx, rdb, keys, gen, LogFormatVersion).Slice()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return []HistorySource{}, nil
	}
	if len(values) != 3 {
		return nil, errors.New("invalid history snapshot")
	}
	generation, ok := values[0].(string)
	base, baseOK := values[1].(int64)
	rows, rowsOK := values[2].([]interface{})
	if !ok || !baseOK || base < 0 || int64(int(base)) != base || !rowsOK {
		return nil, errors.New("invalid history snapshot fields")
	}
	dtos := make([]MemoryDTO, len(rows))
	for i, row := range rows {
		raw, ok := row.(string)
		if !ok || json.Unmarshal([]byte(raw), &dtos[i]) != nil {
			return nil, fmt.Errorf("corrupt history snapshot event at seq %d", base+int64(i))
		}
		if dtos[i].V > LogFormatVersion {
			return nil, fmt.Errorf("unsupported history snapshot version at seq %d", base+int64(i))
		}
	}
	projected := ProjectMessagesWithSources(dtos, int(base))
	messages := make([]*schema.Message, len(projected))
	byMessage := make(map[*schema.Message]HistorySource, len(projected))
	for i, p := range projected {
		messages[i] = p.Message
		id := fmt.Sprintf("history:v1:%d:%s:%d", owner, generation, p.Seq)
		if _, _, err := parseSource(owner, id); err != nil {
			return nil, err
		}
		byMessage[p.Message] = HistorySource{ProjectedSource: p, ID: id}
	}
	trimmed := tokenmeter.TrimToBudget(messages, maxTokens)
	result := make([]HistorySource, 0, len(trimmed))
	for _, msg := range trimmed {
		source, ok := byMessage[msg]
		if !ok {
			return nil, errors.New("trimmed history message lost provenance")
		}
		result = append(result, source)
	}
	return result, nil
}
