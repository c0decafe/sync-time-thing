package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mnm/sync-time-thing/internal/domain"
)

type fakeStore struct {
	settings    domain.Settings
	rules       []domain.Rule
	runs        []domain.RuleRun
	markCalls   []int64
	settingsErr error
	rulesErr    error
	recordErr   error
	markErr     error
}

func (f *fakeStore) GetSettings(context.Context) (domain.Settings, error) {
	return f.settings, f.settingsErr
}
func (f *fakeStore) ListRules(context.Context) ([]domain.Rule, error) { return f.rules, f.rulesErr }
func (f *fakeStore) MarkRuleEvaluated(_ context.Context, id int64, _ time.Time) error {
	f.markCalls = append(f.markCalls, id)
	return f.markErr
}
func (f *fakeStore) RecordRuleRun(_ context.Context, run domain.RuleRun) error {
	f.runs = append(f.runs, run)
	return f.recordErr
}

type fakeExecutor struct {
	err   error
	calls int
}

func (f *fakeExecutor) Execute(context.Context, domain.Settings, domain.Rule) error {
	f.calls++
	return f.err
}

func TestNewUsesDefaultClock(t *testing.T) {
	service := New(&fakeStore{}, &fakeExecutor{}, nil)
	if service == nil || service.now == nil {
		t.Fatal("expected scheduler to initialize defaults")
	}
}

func TestTickSuccessAndSkipDisabled(t *testing.T) {
	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	store := &fakeStore{
		settings: domain.Settings{Timezone: "UTC"},
		rules: []domain.Rule{
			{ID: 1, Name: "due", Schedule: "0 15 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, TargetName: "All devices", Enabled: true, UpdatedAt: now.Add(-time.Hour), LastEvaluatedAt: ptr(now.Add(-30 * time.Minute))},
			{ID: 2, Name: "disabled", Schedule: "0 15 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, Enabled: false, UpdatedAt: now.Add(-time.Hour)},
		},
	}
	executor := &fakeExecutor{}
	service := New(store, executor, func() time.Time { return now })
	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned error: %v", err)
	}
	if executor.calls != 1 || len(store.runs) != 1 || len(store.markCalls) != 1 || store.markCalls[0] != 1 {
		t.Fatalf("unexpected scheduler state: executor=%d runs=%d marks=%v", executor.calls, len(store.runs), store.markCalls)
	}
}

func TestTickRecordsErrors(t *testing.T) {
	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	store := &fakeStore{
		settings: domain.Settings{Timezone: "UTC"},
		rules: []domain.Rule{
			{ID: 1, Name: "bad", Schedule: "not a cron", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, Enabled: true, UpdatedAt: now.Add(-time.Hour)},
			{ID: 2, Name: "impossible", Schedule: "0 0 31 2 *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, Enabled: true, UpdatedAt: now.Add(-time.Hour)},
			{ID: 3, Name: "exec", Schedule: "0 15 * * *", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, Enabled: true, UpdatedAt: now.Add(-time.Hour), LastEvaluatedAt: ptr(now.Add(-30 * time.Minute))},
		},
	}
	executor := &fakeExecutor{err: errors.New("boom")}
	service := New(store, executor, func() time.Time { return now })
	err := service.Tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rule 1") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected Tick error: %v", err)
	}
	if len(store.runs) != 3 || len(store.markCalls) != 3 {
		t.Fatalf("expected errors to be recorded and marked, got runs=%d marks=%d", len(store.runs), len(store.markCalls))
	}
}

func TestTickLoadFailures(t *testing.T) {
	service := New(&fakeStore{settingsErr: errors.New("settings")}, &fakeExecutor{}, func() time.Time { return time.Now() })
	if err := service.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "settings") {
		t.Fatalf("unexpected settings error: %v", err)
	}

	service = New(&fakeStore{settings: domain.Settings{Timezone: "Mars/Olympus"}}, &fakeExecutor{}, func() time.Time { return time.Now() })
	if err := service.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Fatalf("unexpected timezone error: %v", err)
	}

	service = New(&fakeStore{settings: domain.Settings{}, rulesErr: errors.New("rules")}, &fakeExecutor{}, func() time.Time { return time.Now() })
	if err := service.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "rules") {
		t.Fatalf("unexpected rules error: %v", err)
	}
}

func ptr(value time.Time) *time.Time { return &value }

func TestExecutorFuncAndStoreFailures(t *testing.T) {
	called := false
	fn := ExecutorFunc(func(context.Context, domain.Settings, domain.Rule) error {
		called = true
		return nil
	})
	if err := fn.Execute(context.Background(), domain.Settings{}, domain.Rule{}); err != nil {
		t.Fatalf("ExecutorFunc returned error: %v", err)
	}
	if !called {
		t.Fatal("expected ExecutorFunc to be called")
	}

	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)
	store := &fakeStore{
		settings:  domain.Settings{Timezone: "UTC"},
		rules:     []domain.Rule{{ID: 1, Name: "bad", Schedule: "not a cron", Action: domain.ActionPause, TargetKind: domain.TargetGlobal, Enabled: true, UpdatedAt: now.Add(-time.Hour)}},
		recordErr: errors.New("record"),
	}
	service := New(store, &fakeExecutor{}, func() time.Time { return now })
	if err := service.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "record") {
		t.Fatalf("unexpected record error: %v", err)
	}

	store = &fakeStore{
		settings: domain.Settings{Timezone: "UTC"},
		rules: []domain.Rule{{
			ID:              1,
			Name:            "due",
			Schedule:        "0 15 * * *",
			Action:          domain.ActionPause,
			TargetKind:      domain.TargetGlobal,
			TargetName:      "All devices",
			Enabled:         true,
			UpdatedAt:       now.Add(-time.Hour),
			LastEvaluatedAt: ptr(now.Add(-30 * time.Minute)),
		}},
		markErr: errors.New("mark"),
	}
	service = New(store, &fakeExecutor{}, func() time.Time { return now })
	if err := service.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "mark") {
		t.Fatalf("unexpected mark error: %v", err)
	}
}

func TestAdditionalProcessRuleErrorBranches(t *testing.T) {
	now := time.Date(2026, time.March, 30, 15, 0, 0, 0, time.UTC)

	store := &fakeStore{
		settings: domain.Settings{Timezone: "UTC"},
		rules: []domain.Rule{{
			ID:              1,
			Name:            "due",
			Schedule:        "0 15 * * *",
			Action:          domain.ActionPause,
			TargetKind:      domain.TargetGlobal,
			TargetName:      "All devices",
			Enabled:         true,
			UpdatedAt:       now.Add(-time.Hour),
			LastEvaluatedAt: ptr(now.Add(-30 * time.Minute)),
		}},
		recordErr: errors.New("record"),
	}
	service := New(store, &fakeExecutor{}, func() time.Time { return now })
	if err := service.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "record") {
		t.Fatalf("unexpected record error: %v", err)
	}

	store = &fakeStore{
		settings: domain.Settings{Timezone: "UTC"},
		rules: []domain.Rule{{
			ID:         2,
			Name:       "bad",
			Schedule:   "not a cron",
			Action:     domain.ActionPause,
			TargetKind: domain.TargetGlobal,
			Enabled:    true,
			UpdatedAt:  now.Add(-time.Hour),
		}},
		markErr: errors.New("mark"),
	}
	service = New(store, &fakeExecutor{}, func() time.Time { return now })
	if err := service.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "mark") {
		t.Fatalf("unexpected recordError mark failure: %v", err)
	}
}
