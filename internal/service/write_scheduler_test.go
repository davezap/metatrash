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
	const count = 12
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			text := fmt.Sprintf("line-%02d\n", i)
			_, err := s.Dispatch(ctx, "public", "append", Input{Path: "parallel.txt", Text: &text})
			results <- err
		}()
	}
	for i := 0; i < count; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	value, err := s.Dispatch(ctx, "public", "read", Input{Path: "parallel.txt"})
	if err != nil {
		t.Fatal(err)
	}
	text := value.(ReadResult).File.Text
	for i := 0; i < count; i++ {
		if strings.Count(text, fmt.Sprintf("line-%02d\n", i)) != 1 {
			t.Fatalf("lost/duplicated append %d: %q", i, text)
		}
	}
}
