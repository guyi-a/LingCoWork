package skillhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

// catalogServer serves a two-page catalog so pagination in ensureCatalog is
// exercised.
func catalogServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	makeSkill := func(i int) map[string]any {
		return map[string]any{
			"id": fmt.Sprint(i), "slug": fmt.Sprintf("skill-%d", i),
			"fullSlug": fmt.Sprintf("skill-%d", i), "name": fmt.Sprintf("skill-%d", i),
			"description": "does things", "latestVersion": "1.0.0",
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		page := r.URL.Query().Get("page")
		items := []map[string]any{}
		if page == "1" {
			for i := 0; i < catalogPageSize; i++ {
				items = append(items, makeSkill(i))
			}
		} else if page == "2" {
			items = append(items, makeSkill(catalogPageSize))
		}
		pageNum, _ := strconv.Atoi(page)
		blob, _ := json.Marshal(map[string]any{
			"success": true,
			"data": map[string]any{
				"items": items, "authorProfiles": map[string]any{},
				"total": catalogPageSize + 1, "page": pageNum, "pageSize": catalogPageSize,
			},
		})
		w.Write(blob)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func evenOddClassifier(t *testing.T, calls *int) Classifier {
	return func(ctx context.Context, items []Skill) (map[string][]string, error) {
		*calls++
		if len(items) > classifyBatchSize {
			t.Errorf("batch of %d exceeds classifyBatchSize", len(items))
		}
		out := map[string][]string{}
		for i, s := range items {
			if i%2 == 0 {
				out[s.FullSlug] = []string{"dev-debug"}
			}
			// odd items intentionally unanswered -> fallback to other
		}
		return out, nil
	}
}

func TestCategoriesCountsAndCache(t *testing.T) {
	var registryCalls, classifierCalls int
	srv := catalogServer(t, &registryCalls)
	cachePath := filepath.Join(t.TempDir(), "cats.json")

	c := NewCategories(NewRegistry(srv.URL), cachePath, evenOddClassifier(t, &classifierCalls))
	counts, err := c.Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	byID := map[string]int{}
	for _, cc := range counts {
		byID[cc.ID] = cc.Count
	}
	total := catalogPageSize + 1
	if byID["dev-debug"]+byID["other"] != total {
		t.Fatalf("dev-debug(%d)+other(%d) != %d", byID["dev-debug"], byID["other"], total)
	}
	if classifierCalls == 0 {
		t.Fatal("classifier never called")
	}

	// A fresh service with the same cache file must not call the classifier.
	classifierCalls = 0
	failing := Classifier(func(context.Context, []Skill) (map[string][]string, error) {
		return nil, errors.New("must not be called")
	})
	c2 := NewCategories(NewRegistry(srv.URL), cachePath, failing)
	if _, err := c2.Counts(context.Background()); err != nil {
		t.Fatalf("Counts with warm cache: %v", err)
	}
}

func TestListByCategoryFiltersAndPaginates(t *testing.T) {
	var registryCalls, classifierCalls int
	srv := catalogServer(t, &registryCalls)
	c := NewCategories(NewRegistry(srv.URL), filepath.Join(t.TempDir(), "c.json"), evenOddClassifier(t, &classifierCalls))

	page, err := c.ListByCategory(context.Background(), "dev-debug", "", 1, 10)
	if err != nil {
		t.Fatalf("ListByCategory: %v", err)
	}
	if len(page.Items) != 10 {
		t.Fatalf("items = %d, want 10", len(page.Items))
	}
	if page.Total == 0 {
		t.Fatal("total should be > 0")
	}
	for _, s := range page.Items {
		if got := c.assignments[s.FullSlug]; len(got) == 0 || got[0] != "dev-debug" {
			t.Fatalf("item %s not in dev-debug: %v", s.FullSlug, got)
		}
	}

	// Search on top of the category.
	filtered, err := c.ListByCategory(context.Background(), "dev-debug", "skill-0", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total == 0 {
		t.Fatal("query should still match skill-0")
	}

	if _, err := c.ListByCategory(context.Background(), "nope", "", 1, 10); err == nil {
		t.Fatal("unknown category must error")
	}
}

func TestCategoriesClassifierFailureSurfaces(t *testing.T) {
	var registryCalls int
	srv := catalogServer(t, &registryCalls)
	c := NewCategories(NewRegistry(srv.URL), filepath.Join(t.TempDir(), "c.json"), func(context.Context, []Skill) (map[string][]string, error) {
		return nil, errors.New("no api key")
	})
	if _, err := c.Counts(context.Background()); err == nil {
		t.Fatal("classifier failure must surface so the UI can hide the category bar")
	}
}

func TestParseClassifierResponse(t *testing.T) {
	got, err := parseClassifierResponse("噪音 [{\"slug\":\"a\",\"categories\":[\"dev-debug\",\"docs-knowledge\",\"other\"]},{\"slug\":\"b\",\"categories\":[\"bogus\"]}] 尾巴")
	if err != nil {
		t.Fatal(err)
	}
	if len(got["a"]) != 2 || got["a"][0] != "dev-debug" {
		t.Fatalf("a = %v (want capped at 2, order kept)", got["a"])
	}
	if len(got["b"]) != 1 || got["b"][0] != "other" {
		t.Fatalf("b = %v (unknown ids must fall back to other)", got["b"])
	}
	if _, err := parseClassifierResponse("no json here"); err == nil {
		t.Fatal("garbage must error")
	}
}
