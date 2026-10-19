package spill

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// Page is a slice of stored text, counted in Unicode code points.
type Page struct {
	Text    string `json:"text"`
	Offset  int    `json:"offset"`
	Next    int    `json:"next"`
	Total   int    `json:"total"`
	HasMore bool   `json:"has_more"`
}

// ReadPage is the owner-bound API for Redis locators. The caller supplies owner
// from its trusted context. Existing Store/LoadText semantics are unchanged.
func (s RedisStore) ReadPage(ctx context.Context, owner uint, locator string, offset, limit int) (Page, error) {
	parts := strings.Split(locator, ":")
	if len(parts) != 3 || parts[0] != "spill" || parts[1] != strconv.FormatUint(uint64(owner), 10) {
		return Page{}, errors.New("spill source unavailable for owner")
	}
	id, err := hex.DecodeString(parts[2])
	if err != nil || len(id) != 16 {
		return Page{}, errors.New("invalid spill locator")
	}
	if offset < 0 || limit < 1 || limit > 2000 {
		return Page{}, errors.New("invalid spill page: offset >= 0, limit 1..2000")
	}
	content, err := s.LoadText(ctx, locator)
	if err != nil {
		return Page{}, err
	}
	runes := []rune(content)
	if offset > len(runes) {
		return Page{}, errors.New("spill offset beyond content")
	}
	end := offset + min(limit, len(runes)-offset)
	return Page{Text: string(runes[offset:end]), Offset: offset, Next: end, Total: len(runes), HasMore: end < len(runes)}, nil
}
