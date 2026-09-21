package rag_test

import (
	"context"
	"reflect"
	"testing"

	"go_im_gateway/test/rag"
)

func TestRAGEvaluationQueriesAndRanking(t *testing.T) {
	if got := rag.QueryTerms("中文测试 Foo.Bar 中文"); !reflect.DeepEqual(got, []string{"Foo.Bar", "中文", "文测", "测试"}) {
		t.Fatalf("terms %v", got)
	}
	ids, err := rag.Rank(context.Background(), "alpha beta", func(_ context.Context, term string) ([]string, error) {
		if term == "alpha" {
			return []string{"S02", "S02", "S01"}, nil
		}
		return []string{"S01", "S03"}, nil
	})
	if err != nil || !reflect.DeepEqual(ids, []string{"S01", "S02", "S03"}) {
		t.Fatalf("rank %v %v", ids, err)
	}
}

func TestRAGEvaluationScoringAndHoldout(t *testing.T) {
	calls := 0
	cases := []rag.Case{{ID: "one", Split: "dev", Question: "question", Sources: []string{"S01", "S02"}}, {ID: "none", Split: "dev", Question: "unknown"}, {ID: "hidden", Split: "holdout", Question: "must not query"}}
	result, err := rag.Evaluate(context.Background(), cases, func(_ context.Context, q string) ([]string, error) {
		calls++
		if q == "must not query" {
			t.Fatal("holdout was used")
		}
		return []string{"S01", "S01"}, nil
	})
	if err != nil || calls != 2 || result.Answerable != 1 || result.MeanRecall != 0.5 || result.Complete != 0 || result.NoAnswerWithCandidates != 1 || len(result.Cases) != 2 {
		t.Fatalf("scores %+v %v", result, err)
	}
	if !reflect.DeepEqual(result.Cases[0].Missing, []string{"S02"}) {
		t.Fatal("missing sources lost")
	}
}
