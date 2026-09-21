//go:build knowledgeintegration

package knowledge_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"go_im_gateway/internal/knowledge"
	"gorm.io/gorm"
)

func TestKnowledgeChunksImportAndRebuild(t *testing.T) {
	s := knowledge.NewStore(database(t))
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "chunks")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("原文🧭\r\n", 500)
	src, _, err := s.Import(ctx, 11, lib.ID, "source.md", "md", body)
	if err != nil {
		t.Fatal(err)
	}
	status, err := s.SourceIndex(ctx, 11, lib.ID, src.ID)
	if err != nil || status.State != "ready" || status.Index.RuleVersion != knowledge.ChunkRuleVersion || status.Index.ChunkCount < 2 {
		t.Fatalf("index %+v %v", status, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.RebuildIndex(ctx, 11, lib.ID, src.ID); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	chunks, err := s.Chunks(ctx, 11, lib.ID, src.ID, src.CurrentVersionID, 0)
	if err != nil || len(chunks) != status.Index.ChunkCount {
		t.Fatalf("chunks %d %v", len(chunks), err)
	}
	for _, chunk := range chunks {
		if chunk.Content != string([]rune(body)[chunk.Start:chunk.End]) {
			t.Fatal("coordinate mismatch")
		}
	}
	if _, err := s.RebuildIndex(ctx, 22, lib.ID, src.ID); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("owner %v", err)
	}
}

func TestKnowledgeChunksFailureAtomicity(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "chunks")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := s.Import(ctx, 11, lib.ID, "good.md", "md", strings.Repeat("original", 300))
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Chunks(ctx, 11, lib.ID, src.ID, src.CurrentVersionID, 0)
	if err != nil || len(before) == 0 {
		t.Fatalf("before %v", err)
	}
	for _, table := range []string{"knowledge_chunks", "knowledge_chunk_indexes"} {
		t.Run(table, func(t *testing.T) {
			if err := db.Callback().Create().Before("gorm:create").Register("test:index_fail", func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(errors.New("injected indexing failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Create().Remove("test:index_fail")
			if _, err := s.RebuildIndex(ctx, 11, lib.ID, src.ID); err == nil {
				t.Fatal("rebuild failure hidden")
			}
			after, err := s.Chunks(ctx, 11, lib.ID, src.ID, src.CurrentVersionID, 0)
			if err != nil || len(after) != len(before) || after[0].ID != before[0].ID {
				t.Fatalf("old index destroyed %v", err)
			}
			if _, _, err := s.Import(ctx, 11, lib.ID, "failed.md", "md", "new content"); err == nil {
				t.Fatal("partial import accepted")
			}
			items, err := s.Sources(ctx, 11, lib.ID, 0, 50)
			if err != nil || len(items) != 1 {
				t.Fatalf("failed import survived %v", err)
			}
		})
	}
}

func TestKnowledgeChunksHTTPAndExistingData(t *testing.T) {
	db := database(t)
	s := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := s.CreateLibrary(ctx, 11, "chunks")
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := s.Import(ctx, 11, lib.ID, "existing.md", "md", strings.Repeat("x", 36000))
	if err != nil {
		t.Fatal(err)
	}
	// Represent a pre-v4 source without touching its immutable original.
	if err := db.Where("version_id = ?", src.CurrentVersionID).Delete(&knowledge.Chunk{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("version_id = ?", src.CurrentVersionID).Delete(&knowledge.ChunkIndex{}).Error; err != nil {
		t.Fatal(err)
	}
	status, err := s.SourceIndex(ctx, 11, lib.ID, src.ID)
	if err != nil || status.State != "missing" {
		t.Fatalf("missing %+v %v", status, err)
	}
	request := func(owner uint, method, path string) *httptest.ResponseRecorder {
		r := gin.New()
		g := r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", owner) })
		knowledge.Register(g, s)
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest(method, path, nil))
		return out
	}
	base := fmt.Sprintf("/api/v1/libraries/%d/sources/%d", lib.ID, src.ID)
	if out := request(22, "POST", base+"/index/rebuild"); out.Code != 404 {
		t.Fatalf("unauthorized rebuild %d", out.Code)
	}
	if out := request(11, "POST", base+"/index/rebuild"); out.Code != 200 {
		t.Fatalf("rebuild %s", out.Body.String())
	}
	page, err := s.Chunks(ctx, 11, lib.ID, src.ID, src.CurrentVersionID, 0)
	if err != nil || len(page) != 50 {
		t.Fatalf("page %d %v", len(page), err)
	}
	next, err := s.Chunks(ctx, 11, lib.ID, src.ID, src.CurrentVersionID, 50)
	if err != nil || len(next) == 0 || next[0].Ordinal != 51 {
		t.Fatalf("next %v", err)
	}
	path := fmt.Sprintf("%s/versions/%d/chunks", base, src.CurrentVersionID)
	for _, tc := range []struct {
		owner  uint
		path   string
		status int
	}{{11, path, 200}, {22, path, 404}, {11, path + "?after=-1", 400}, {11, fmt.Sprintf("%s/versions/%d/chunks", base, src.CurrentVersionID+100), 404}} {
		if out := request(tc.owner, "GET", tc.path); out.Code != tc.status {
			t.Fatalf("status %d want %d", out.Code, tc.status)
		}
	}
}
