//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"strings"
	"testing"

	"go_im_gateway/internal/knowledge"
)

func TestKnowledgeChunkSearchBoundaryAndFallback(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "boundaries")
	if err != nil {
		t.Fatal(err)
	}
	short := strings.Repeat("界", 100)
	long := strings.Repeat("长", 128)
	// The 128-character match starts before window two, but ends after window
	// one. Chunk-only search would miss it; the long-query fallback must not.
	body := strings.Repeat("中", 699) + long + strings.Repeat("x", 670) + short + "尾部"
	src, _, err := s.Import(ctx, 11, lib.ID, "boundary.md", "md", body)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query  string
		offset int
		rule   string
	}{{long, 699, ""}, {short, 1497, knowledge.ChunkRuleVersion}, {"长长", 699, knowledge.ChunkRuleVersion}} {
		result, err := s.SearchSources(ctx, 11, lib.ID, tc.query, 0)
		if err != nil || len(result.Items) != 1 {
			t.Fatalf("result %+v %v", result, err)
		}
		hit := result.Items[0]
		if hit.Offset != tc.offset || hit.IndexRule != tc.rule || string([]rune(body)[hit.Offset:hit.MatchEnd]) != tc.query {
			t.Fatalf("position or route %+v", hit)
		}
	}
	if err := db.Model(&knowledge.ChunkIndex{}).Where("version_id = ?", src.CurrentVersionID).Update("rule_version", "old-rule").Error; err != nil {
		t.Fatal(err)
	}
	result, err := s.SearchSources(ctx, 11, lib.ID, short, 0)
	if err != nil || len(result.Items) != 1 || result.Items[0].IndexRule != "" || result.Items[0].Offset != 1497 {
		t.Fatalf("outdated fallback %+v %v", result, err)
	}
	if err := db.Where("version_id = ?", src.CurrentVersionID).Delete(&knowledge.ChunkIndex{}).Error; err != nil {
		t.Fatal(err)
	}
	result, err = s.SearchSources(ctx, 11, lib.ID, short, 0)
	if err != nil || len(result.Items) != 1 || result.Items[0].IndexRule != "" {
		t.Fatalf("missing fallback %+v %v", result, err)
	}
	if _, err := s.RebuildIndex(ctx, 11, lib.ID, src.ID); err != nil {
		t.Fatal(err)
	}
	result, err = s.SearchSources(ctx, 11, lib.ID, short, 0)
	if err != nil || len(result.Items) != 1 || result.Items[0].IndexRule != knowledge.ChunkRuleVersion {
		t.Fatalf("rebuilt route %+v %v", result, err)
	}
}

func TestKnowledgeChunkSearchMixedPagination(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "mixed")
	if err != nil {
		t.Fatal(err)
	}
	ids := []uint64{}
	for i := 0; i < 12; i++ {
		// Two matches and overlapping chunks must still produce one source hit.
		body := strings.Repeat("x", 750) + "needle" + strings.Repeat("中", i+900) + "needle"
		src, _, err := s.Import(ctx, 11, lib.ID, "mixed.md", "md", body)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, src.ID)
		if i%2 == 1 {
			if err := db.Where("version_id = ?", src.CurrentVersionID).Delete(&knowledge.ChunkIndex{}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := s.SearchSources(ctx, 11, lib.ID, "needle", 0)
	if err != nil || len(page.Items) != 10 || page.NextAfter != ids[9] {
		t.Fatalf("page %+v %v", page, err)
	}
	next, err := s.SearchSources(ctx, 11, lib.ID, "needle", page.NextAfter)
	if err != nil || len(next.Items) != 2 || next.NextAfter != 0 {
		t.Fatalf("next %+v %v", next, err)
	}
	for i, hit := range append(page.Items, next.Items...) {
		if hit.SourceID != ids[i] || hit.Offset != 750 {
			t.Fatalf("missing or duplicate hit %+v", hit)
		}
		if (hit.IndexRule != "") != (i%2 == 0) {
			t.Fatalf("wrong route %+v", hit)
		}
	}
}
