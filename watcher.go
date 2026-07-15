package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title,omitempty"`
	State     string    `json:"state,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}
type IssueSource interface {
	ListIssues(context.Context, string) ([]Issue, error)
}
type AgentRunner interface {
	Run(context.Context, Issue) error
}
type Watcher struct {
	Repo        string
	Interval    time.Duration
	Concurrency int
	StatePath   string
	Issues      IssueSource
	Runner      AgentRunner
	Out         io.Writer
}

func (w *Watcher) Run(ctx context.Context) error {
	if !validRepo(w.Repo) {
		return fmt.Errorf("repository must be OWNER/REPO")
	}
	if w.Interval <= 0 {
		return fmt.Errorf("interval must be positive")
	}
	if w.Concurrency <= 0 {
		return fmt.Errorf("concurrency must be positive")
	}
	if w.Out == nil {
		w.Out = io.Discard
	}
	seen, err := loadState(w.StatePath)
	if err != nil {
		return err
	}
	first := len(seen) == 0
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	poll := func() error {
		issues, err := w.Issues.ListIssues(ctx, w.Repo)
		if err != nil {
			return err
		}
		newIssues := make([]Issue, 0)
		for _, issue := range issues {
			if issue.Number > 0 && !seen[issue.Number] {
				seen[issue.Number] = true
				newIssues = append(newIssues, issue)
			}
		}
		if err := saveState(w.StatePath, seen); err != nil {
			return err
		}
		if first {
			first = false
			return nil
		}
		for _, issue := range newIssues {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			wg.Add(1)
			go func(i Issue) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := w.Runner.Run(ctx, i); err != nil {
					fmt.Fprintf(w.Out, "issue #%d: %v\n", i.Number, err)
				} else {
					fmt.Fprintf(w.Out, "issue #%d completed\n", i.Number)
				}
			}(issue)
		}
		return nil
	}
	if err := poll(); err != nil {
		if ctx.Err() != nil {
			wg.Wait()
			return nil
		}
		return err
	}
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
			if err := poll(); err != nil {
				if ctx.Err() != nil {
					wg.Wait()
					return nil
				}
				fmt.Fprintf(w.Out, "poll: %v\n", err)
			}
		}
	}
}
func parseIssues(data []byte) ([]Issue, error) {
	var issues []Issue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("decode issues: %w", err)
	}
	return issues, nil
}
func loadState(path string) (map[int]bool, error) {
	if path == "" {
		return map[int]bool{}, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[int]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s map[int]bool
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if s == nil {
		s = map[int]bool{}
	}
	return s, nil
}
func saveState(path string, seen map[int]bool) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
