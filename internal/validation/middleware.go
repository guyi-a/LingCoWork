package validation

import (
	"context"

	"github.com/cloudwego/eino/compose"
)

func (s *Service) Middleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				out, err := next(ctx, input)
				if err != nil || out == nil {
					return out, err
				}
				out.Result = s.Enrich(ctx, input, out.Result)
				return out, nil
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			// run_command is invokable today. Leave streamable tools untouched
			// rather than buffering arbitrary streams just for enrichment.
			return next
		},
	}
}
