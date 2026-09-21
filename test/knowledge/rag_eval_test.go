//go:build knowledgeintegration && rageval

package knowledge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go_im_gateway/internal/knowledge"
	"go_im_gateway/test/rag"
)

func TestRAGDevelopmentEvaluation(t *testing.T) {
	output := os.Getenv("KNOWLEDGE_RAG_REPORT")
	if output == "" {
		t.Fatal("use scripts/eval-rag.sh to select a report path")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate corpus")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	workspace := filepath.Dir(repo)
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	manifestBytes := read(filepath.Join(repo, "test/rag/manifest.json"))
	var manifest struct {
		Version string `json:"version"`
		Sources []struct {
			ID     string `json:"id"`
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version == "" || len(manifest.Sources) == 0 {
		t.Fatal("empty corpus manifest")
	}
	bodies := map[string]string{}
	for _, source := range manifest.Sources {
		if source.ID == "" || bodies[source.ID] != "" || filepath.IsAbs(source.Path) {
			t.Fatal("invalid corpus source")
		}
		path := filepath.Join(workspace, filepath.FromSlash(source.Path))
		rel, err := filepath.Rel(workspace, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatal("corpus path leaves workspace")
		}
		data := read(path)
		if !strings.EqualFold(hash(data), source.SHA256) {
			t.Fatalf("corpus drift: %s; do not overwrite the manifest to hide changed content", source.ID)
		}
		bodies[source.ID] = string(data)
	}
	casesBytes := read(filepath.Join(repo, "test/rag/cases.json"))
	var cases struct {
		Version string     `json:"version"`
		Cases   []rag.Case `json:"cases"`
	}
	if err := json.Unmarshal(casesBytes, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases.Cases {
		if c.Split != "dev" {
			continue
		}
		for _, id := range c.Sources {
			if _, ok := bodies[id]; !ok {
				t.Fatalf("unknown gold source %s", id)
			}
		}
	}
	// All corpus validation precedes test DB creation; only source bodies are
	// imported. Questions and gold labels never become searchable documents.
	db := database(t)
	store := knowledge.NewStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	lib, err := store.CreateLibrary(ctx, 11, "K-0 isolated development evaluation")
	if err != nil {
		t.Fatal(err)
	}
	labels := map[uint64]string{}
	for _, source := range manifest.Sources {
		doc, duplicate, err := store.Import(ctx, 11, lib.ID, filepath.Base(source.Path), "md", bodies[source.ID])
		if err != nil || duplicate {
			t.Fatalf("import %s: duplicate=%v err=%v", source.ID, duplicate, err)
		}
		labels[doc.ID] = source.ID
	}
	observations := map[string][]knowledge.KeywordHit{}
	started := time.Now().UTC()
	scores, err := rag.Evaluate(ctx, cases.Cases, func(ctx context.Context, question string) ([]string, error) {
		return rag.Rank(ctx, question, func(ctx context.Context, term string) ([]string, error) {
			result, err := store.SearchSources(ctx, 11, lib.ID, term, 0)
			if err != nil {
				return nil, err
			}
			if result.NextAfter != 0 {
				return nil, fmt.Errorf("evaluation corpus exceeds a single search page; pagination must be added before scoring")
			}
			observations[term] = result.Items
			ids := []string{}
			for _, hit := range result.Items {
				id, ok := labels[hit.SourceID]
				if !ok {
					return nil, fmt.Errorf("unknown retrieved source")
				}
				runes := []rune(bodies[id])
				if hit.Offset < 0 || hit.MatchEnd > len(runes) || hit.MatchEnd <= hit.Offset || string(runes[hit.Offset:hit.MatchEnd]) != term {
					return nil, fmt.Errorf("invalid retrieved position")
				}
				if hit.IndexRule != knowledge.ChunkRuleVersion {
					return nil, fmt.Errorf("evaluation expected a complete current chunk index")
				}
				ids = append(ids, id)
			}
			return ids, nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	implementation := map[string]string{}
	for _, path := range []string{"internal/knowledge/search.go", "internal/knowledge/chunks.go", "test/rag/evaluator.go"} {
		implementation[path] = hash(read(filepath.Join(repo, path)))
	}
	report := map[string]any{"started_at": started, "duration_ms": time.Since(started).Milliseconds(), "corpus": manifest, "cases_version": cases.Version, "cases_sha256": hash(casesBytes), "manifest_sha256": hash(manifestBytes), "query_policy": rag.QueryPolicy, "chunk_rule": knowledge.ChunkRuleVersion, "implementation_sha256": implementation, "split": "dev", "scores": scores, "source_ids": labels, "query_hits": observations, "limitations": "source recall only; no model answer, no answer correctness, no semantic search; holdout excluded"}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// Exclusive creation prevents a failed/new run being confused with an old report.
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	t.Logf("dev=%d answerable=%d recall@5=%.3f complete=%d/%d unanswerable-with-candidates=%d/%d", len(scores.Cases), scores.Answerable, scores.MeanRecall, scores.Complete, scores.Answerable, scores.NoAnswerWithCandidates, scores.NoAnswer)
	for _, row := range scores.Cases {
		if len(row.Missing) > 0 {
			t.Logf("%s missing=%v retrieved=%v", row.ID, row.Missing, row.Retrieved)
		}
	}
	t.Logf("report: %s (execution PASS is not a quality threshold)", output)
}
