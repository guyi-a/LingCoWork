package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/websearch"
)

type webSearchInput struct {
	Query          string   `json:"query" jsonschema:"description=Search query. Keep language consistent with the user (Chinese or English). More specific queries yield better results. Required."`
	Region         string   `json:"region,omitempty" jsonschema:"description=Which region to search. 'cn' = Bocha only (国内话题省 quota); 'global' = Tavily only (海外话题省 quota); 'both' (default) = both providers in parallel\\, deduped. Use 'cn' or 'global' when you're sure about the topic; use 'both' when unsure or the topic spans regions.,enum=cn,enum=global,enum=both"`
	MaxResults     int      `json:"max_results,omitempty" jsonschema:"description=Max results per provider. 1-20\\, default 10. 'both' merges to potentially fewer results after dedupe."`
	Timelimit      string   `json:"timelimit,omitempty" jsonschema:"description=Time filter: 'd' (day) / 'w' (week) / 'm' (month) / 'y' (year). Use for time-sensitive queries like news / recent releases.,enum=d,enum=w,enum=m,enum=y"`
	Topic          string   `json:"topic,omitempty" jsonschema:"description=Search topic: 'finance' / 'news'. Values not in the supported set are silently ignored (won't error). Only affects Tavily.,enum=finance,enum=news"`
	AllowedDomains []string `json:"allowed_domains,omitempty" jsonschema:"description=Only include results from these domains. Example: ['docs.python.org'\\, 'stackoverflow.com']. Mutually exclusive with blocked_domains — set only one. Only affects Tavily."`
	BlockedDomains []string `json:"blocked_domains,omitempty" jsonschema:"description=Exclude results from these domains. Example: ['pinterest.com']. Mutually exclusive with allowed_domains. Only affects Tavily."`
}

type webSearchHit struct {
	Title string `json:"title"`
	Href  string `json:"href"`
	Body  string `json:"body"`
}

type webSearchOutput struct {
	Query  string         `json:"query"`
	Count  int            `json:"count"`
	Hits   []webSearchHit `json:"hits"`
	Notice string         `json:"notice,omitempty"` // count==0 时给 LLM 一句解释
}

func newWebSearchTool(svc *websearch.Service) (tool.BaseTool, error) {
	if svc == nil {
		return nil, errors.New("web_search: nil service")
	}
	fn := func(ctx context.Context, in *webSearchInput) (*webSearchOutput, error) {
		if in == nil {
			return nil, errors.New("nil input")
		}
		results, err := svc.Search(ctx, in.Query, websearch.Options{
			Region:         websearch.Region(in.Region),
			MaxResults:     in.MaxResults,
			Timelimit:      in.Timelimit,
			AllowedDomains: in.AllowedDomains,
			BlockedDomains: in.BlockedDomains,
			Topic:          in.Topic,
		})
		if err != nil {
			return nil, fmt.Errorf("web_search failed: %w", err)
		}

		hits := make([]webSearchHit, len(results))
		for i, r := range results {
			hits[i] = webSearchHit{Title: r.Title, Href: r.Href, Body: r.Body}
		}
		out := &webSearchOutput{
			Query: in.Query,
			Count: len(hits),
			Hits:  hits,
		}
		if len(hits) == 0 {
			out.Notice = "0 results. Try different keywords, drop the timelimit / topic / domain filters, or switch region."
		}
		return out, nil
	}

	desc := "Search the web for information. Returns a list of {title, href, body} hits. " +
		"Use when the user asks about current events, unfamiliar concepts, product / service info, or anything the model's training data may not cover. " +
		"Prefer specific queries over broad ones. " +
		"IMPORTANT: When you use search results in your response, always cite sources via markdown links [title](href) so the user can verify. " +
		"After search, if you need the full page content (not just the snippet), call web_fetch on the href."

	return utils.InferTool("web_search", desc, fn)
}
