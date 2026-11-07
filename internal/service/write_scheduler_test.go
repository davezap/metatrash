package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestSerialWriteConcurrentAppends(t *testing.T) {
	s := openPublicTestService(t)
	ctx := context.Background()
	// Keep each real-Git burst inside the queue-inclusive operation deadline on
	// slower Windows hosts. Gated contract tests cover queue admission/ordering;
	// this integration test checks concurrent appends preserve every line.
	const count, wave = 12, 4
	results := make(chan error, count)
	for start := 0; start < count; start += wave {
		end := min(start+wave, count)
		for i := start; i < end; i++ {
			go func() {
				text := fmt.Sprintf("line-%02d\n", i)
				_, err := s.Dispatch(ctx, "public", "append", Input{Path: "parallel.txt", Text: &text})
				results <- err
			}()
		}
		var firstErr error
		for i := start; i < end; i++ {
			if err := <-results; err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			t.Fatal(firstErr)
		}
	}
	value, err := s.Dispatch(ctx, "public", "read", Input{Path: "parallel.txt"})
	if err != nil {
		t.Fatal(err)
	}
	text := value.(ReadResult).File.Text
	if lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n"); len(lines) != count {
		t.Fatalf("expected %d appended lines, got %d: %q", count, len(lines), text)
	}
	for i := 0; i < count; i++ {
		if strings.Count(text, fmt.Sprintf("line-%02d\n", i)) != 1 {
			t.Fatalf("lost/duplicated append %d: %q", i, text)
		}
	}
}
