// Package rag contains offline evaluation helpers, never production retrieval policy.
package rag

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"unicode"
)

const QueryPolicy = "han-bigram-identifiers-v1-max32"

var identifiers = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_./:-]*`)

// QueryTerms receives only the question, never gold evidence or source labels.
func QueryTerms(question string) []string {
	terms := []string{}
	seen := map[string]bool{}
	add := func(term string) {
		if !seen[term] && len(terms) < 32 {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	for _, term := range identifiers.FindAllString(question, -1) {
		add(term)
	}
	var previous rune
	for _, r := range question {
		if !unicode.Is(unicode.Han, r) {
			previous = 0
			continue
		}
		if previous != 0 {
			add(string([]rune{previous, r}))
		}
		previous = r
	}
	return terms
}

// Rank uses the number of distinct query terms hitting a source, with stable
// source ID tie-breaking. It is a declared evaluation baseline, not BM25.
func Rank(ctx context.Context, question string, search func(context.Context, string) ([]string, error)) ([]string, error) {
	scores := map[string]int{}
	for _, term := range QueryTerms(question) {
		ids, err := search(ctx, term)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !seen[id] {
				scores[id]++
				seen[id] = true
			}
		}
	}
	ids := []string{}
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > 5 {
		ids = ids[:5]
	}
	return ids, nil
}

type Case struct {
	ID       string   `json:"id"`
	Split    string   `json:"split"`
	Question string   `json:"question"`
	Sources  []string `json:"sources"`
}
type CaseResult struct {
	ID        string   `json:"id"`
	Question  string   `json:"question"`
	Terms     []string `json:"terms"`
	Expected  []string `json:"expected_sources"`
	Retrieved []string `json:"retrieved_sources"`
	Missing   []string `json:"missing_sources"`
	Recall    *float64 `json:"recall_at_5,omitempty"`
}
type Scores struct {
	Answerable             int          `json:"answerable_cases"`
	MeanRecall             float64      `json:"macro_source_recall_at_5"`
	Complete               int          `json:"all_expected_sources_found"`
	NoAnswer               int          `json:"unanswerable_cases"`
	NoAnswerWithCandidates int          `json:"unanswerable_with_candidates"`
	Cases                  []CaseResult `json:"cases"`
}

func Evaluate(ctx context.Context, cases []Case, retrieve func(context.Context, string) ([]string, error)) (Scores, error) {
	result := Scores{Cases: []CaseResult{}}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.Split != "dev" {
			continue
		}
		if c.ID == "" || c.Question == "" || seen[c.ID] {
			return Scores{}, fmt.Errorf("invalid development case %q", c.ID)
		}
		seen[c.ID] = true
		ids, err := retrieve(ctx, c.Question)
		if err != nil {
			return Scores{}, fmt.Errorf("case %s: %w", c.ID, err)
		}
		unique := []string{}
		found := map[string]bool{}
		for _, id := range ids {
			if !found[id] && len(unique) < 5 {
				found[id] = true
				unique = append(unique, id)
			}
		}
		row := CaseResult{ID: c.ID, Question: c.Question, Terms: QueryTerms(c.Question), Expected: c.Sources, Retrieved: unique, Missing: []string{}}
		gold := map[string]bool{}
		for _, id := range c.Sources {
			gold[id] = true
		}
		if len(gold) == 0 {
			result.NoAnswer++
			if len(unique) > 0 {
				result.NoAnswerWithCandidates++
			}
		} else {
			for id := range gold {
				if !found[id] {
					row.Missing = append(row.Missing, id)
				}
			}
			sort.Strings(row.Missing)
			recall := float64(len(gold)-len(row.Missing)) / float64(len(gold))
			row.Recall = &recall
			result.Answerable++
			result.MeanRecall += recall
			if len(row.Missing) == 0 {
				result.Complete++
			}
		}
		result.Cases = append(result.Cases, row)
	}
	if len(result.Cases) == 0 {
		return Scores{}, fmt.Errorf("no development cases")
	}
	if result.Answerable > 0 {
		result.MeanRecall /= float64(result.Answerable)
	}
	return result, nil
}
