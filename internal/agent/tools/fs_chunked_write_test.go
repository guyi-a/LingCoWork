package tools

import "testing"

func TestResolveChunkedMode(t *testing.T) {
	tests := []struct {
		name  string
		in    ChunkedWriteInput
		want  string
		isErr bool
	}{
		{"explicit start", ChunkedWriteInput{Mode: "start", Path: "a.md"}, "start", false},
		{"explicit append", ChunkedWriteInput{Mode: "append", SessionID: "s", Content: "x"}, "append", false},
		{"case and padding", ChunkedWriteInput{Mode: " Finish "}, "finish", false},

		// The reported bug: the model dropped mode on a follow-up chunk.
		{"missing mode with session and content is an append",
			ChunkedWriteInput{SessionID: "s", Content: "chapter two"}, "append", false},
		{"missing mode with a path and no session is a start",
			ChunkedWriteInput{Path: "report.md", Content: "chapter one"}, "start", false},

		// finish and abort look identical once mode is gone, so guessing would
		// risk discarding a finished file.
		{"missing mode with only a session is ambiguous",
			ChunkedWriteInput{SessionID: "s"}, "", true},
		{"empty input", ChunkedWriteInput{}, "", true},
		{"unknown mode", ChunkedWriteInput{Mode: "commit", SessionID: "s"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveChunkedMode(&tt.in)
			if tt.isErr {
				if err == nil {
					t.Fatalf("expected an error, got mode %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got mode %q, want %q", got, tt.want)
			}
		})
	}
}
