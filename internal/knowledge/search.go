package knowledge

import (
	"context"
	"strings"
	"unicode/utf8"
)

// KeywordHit locates the first exact match in one current source version.
// It is a candidate for read_source, not evidence that the model read the text.
type KeywordHit struct {
	SourceID  uint64 `json:"source_id"`
	VersionID uint64 `json:"version_id"`
	Version   int    `json:"version"`
	Title     string `json:"title"`
	Offset    int    `json:"offset"`
	MatchEnd  int    `json:"match_end"`
	IndexRule string `json:"index_rule,omitempty"`
}

type KeywordResults struct {
	Query     string       `json:"query"`
	Items     []KeywordHit `json:"items"`
	NextAfter uint64       `json:"next_after"`
}

// SearchSources keeps exact matching while using current chunk indexes where
// their overlap guarantees recall. Missing/old indexes and long queries use the
// source baseline. This is chunk scanning, not an inverted or semantic index.
func (s *Store) SearchSources(ctx context.Context, owner uint, library uint64, query string, after uint64) (KeywordResults, error) {
	query = strings.TrimSpace(query)
	result := KeywordResults{Query: query, Items: []KeywordHit{}}
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 128 || strings.ContainsRune(query, 0) {
		return result, ErrInvalid
	}
	db := s.db.WithContext(ctx)
	if err := owned(db, owner, library, false); err != nil {
		return result, err
	}
	// LOCATE on binary strings gives byte offsets. Convert the matched prefix
	// back to UTF-8 before CHAR_LENGTH so positions remain Unicode code points.
	const sourceSQL = `SELECT s.id AS source_id, v.id AS version_id, v.version, s.title,
 CHAR_LENGTH(CONVERT(LEFT(CAST(v.body AS BINARY), LOCATE(CAST(? AS BINARY), CAST(v.body AS BINARY))-1) USING utf8mb4)) AS match_offset,
 '' AS index_rule
 FROM knowledge_sources s
 JOIN knowledge_source_versions v ON v.id=s.current_version_id AND v.source_id=s.id
 LEFT JOIN knowledge_chunk_indexes i ON i.version_id=v.id
 WHERE s.library_id=? AND s.id>? AND LOCATE(CAST(? AS BINARY),CAST(v.body AS BINARY))>0`
	statement := sourceSQL
	args := []any{query, library, after, query}
	if utf8.RuneCountInString(query) <= chunkOverlap {
		const chunksSQL = `SELECT s.id AS source_id, v.id AS version_id, v.version, s.title,
 MIN(c.start_offset + CHAR_LENGTH(CONVERT(LEFT(CAST(c.content AS BINARY), LOCATE(CAST(? AS BINARY),CAST(c.content AS BINARY))-1) USING utf8mb4))) AS match_offset,
 i.rule_version AS index_rule
 FROM knowledge_sources s
 JOIN knowledge_source_versions v ON v.id=s.current_version_id AND v.source_id=s.id
 JOIN knowledge_chunk_indexes i ON i.version_id=v.id
 JOIN knowledge_chunks c ON c.version_id=v.id
 WHERE s.library_id=? AND s.id>? AND i.rule_version=? AND LOCATE(CAST(? AS BINARY),CAST(c.content AS BINARY))>0
 GROUP BY s.id,v.id,v.version,s.title,i.rule_version`
		statement = chunksSQL + " UNION ALL " + sourceSQL + " AND (i.version_id IS NULL OR i.rule_version<>?)"
		args = []any{query, library, after, ChunkRuleVersion, query, query, library, after, query, ChunkRuleVersion}
	}
	var rows []struct {
		SourceID    uint64
		VersionID   uint64
		Version     int
		Title       string
		MatchOffset int
		IndexRule   string
	}
	err := db.Raw("SELECT * FROM ("+statement+") AS candidates ORDER BY source_id ASC LIMIT 11", args...).Scan(&rows).Error
	if err != nil {
		return result, err
	}
	if len(rows) > 10 {
		rows = rows[:10]
		result.NextAfter = rows[9].SourceID
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Items = append(result.Items, KeywordHit{SourceID: row.SourceID, VersionID: row.VersionID, Version: row.Version, Title: row.Title, Offset: row.MatchOffset, MatchEnd: row.MatchOffset + utf8.RuneCountInString(query), IndexRule: row.IndexRule})
	}
	return result, nil
}
