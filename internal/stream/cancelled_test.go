package stream

import "testing"

func TestIsCanceledResult(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "denial payload",
			content: `{"canceled":true,"tool":"run_command","instruction":"用户拒绝执行该工具"}`,
			want:    true,
		},
		{
			name:    "placeholder for a call that never ran",
			content: "[canceled] tool did not run",
			want:    true,
		},
		{
			name:    "leading whitespace",
			content: "  \n{\"canceled\": true}",
			want:    true,
		},
		{
			name:    "empty",
			content: "",
			want:    false,
		},
		// The regression this whole field exists for: a tool that ran fine and
		// returned prose mentioning cancellation. The old substring rule
		// labelled every one of these cancelled.
		{
			name:    "prose about cancellation",
			content: "The request was cancelled by the upstream server, so I retried and it succeeded.",
			want:    false,
		},
		{
			name:    "docs listing a cancel API",
			content: `{"tools":[{"name":"cancel_order","description":"Cancels an order"}]}`,
			want:    false,
		},
		{
			name:    "canceled nested rather than top level",
			content: `{"result":{"canceled":true}}`,
			want:    false,
		},
		{
			name:    "canceled present but false",
			content: `{"canceled":false,"tool":"run_command"}`,
			want:    false,
		},
		{
			name:    "canceled as a string, not a bool",
			content: `{"canceled":"true"}`,
			want:    false,
		},
		{
			name:    "json array, not our envelope",
			content: `[{"canceled":true}]`,
			want:    false,
		},
		{
			name:    "malformed json that mentions the key",
			content: `{"canceled":true`,
			want:    false,
		},
		{
			name:    "the word appears mid-object but not as our key",
			content: `{"status":"canceled"}`,
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCanceledResult(tc.content); got != tc.want {
				t.Errorf("IsCanceledResult(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}
