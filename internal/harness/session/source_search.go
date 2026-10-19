package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
)

type SourceSearch struct {
	Sources            []SourcePage `json:"sources"`
	Next               string       `json:"next_cursor,omitempty"`
	HasMore            bool         `json:"has_more"`
	Scanned            int          `json:"scanned"`
	ExternalUnsearched int          `json:"external_unsearched"`
}

// SearchSources scans at most 50 raw events, returning at most 5 matches in
// newest-first order. Exact, case-sensitive matching supports Chinese and code
// without tokenization. Only persisted Content is searched, never external text.
func (RedisStore) SearchSources(ctx context.Context, owner uint, query, cursor string) (SourceSearch, error) {
	result := SourceSearch{Sources: []SourcePage{}}
	if strings.TrimSpace(query) == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 100 {
		return result, errors.New("query must contain 1..100 Unicode characters")
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
	values, err := listSourceScript.Run(ctx, rdb, sourceKeys(owner), gen, expected, seq, 50).StringSlice()
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
		if err := ctx.Err(); err != nil {
			return SourceSearch{}, err
		}
		seq := start + int64(i-2)
		var dto MemoryDTO
		if err := json.Unmarshal([]byte(values[i]), &dto); err != nil {
			return SourceSearch{}, fmt.Errorf("corrupt source at seq %d", seq)
		}
		if dto.V > LogFormatVersion {
			return SourceSearch{}, errors.New("unsupported source log version")
		}
		result.Scanned++
		if dto.SpillLocator != "" {
			result.ExternalUnsearched++
		}
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		if typ == EventUserMessage || typ == EventAssistantMessage || typ == EventToolResult || typ == EventCompactionSummary {
			if index := strings.Index(dto.Content, query); index >= 0 {
				offset := max(0, utf8.RuneCountInString(dto.Content[:index])-40)
				page, err := sourcePage(owner, values[0], seq, values[i], offset, 200)
				if err != nil {
					return SourceSearch{}, err
				}
				result.Sources = append(result.Sources, page)
			}
		}
		result.HasMore = seq > 0
		if result.HasMore {
			result.Next = fmt.Sprintf("history:v1:%d:%s:%d", owner, values[0], seq)
		} else {
			result.Next = ""
		}
		if len(result.Sources) == 5 {
			break
		}
	}
	return result, nil
}
