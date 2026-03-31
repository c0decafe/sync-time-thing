package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mnm/sync-time-thing/internal/cronexpr"
	"github.com/mnm/sync-time-thing/internal/domain"
)

type Store interface {
	GetSettings(ctx context.Context) (domain.Settings, error)
	ListRules(ctx context.Context) ([]domain.Rule, error)
	MarkRuleEvaluated(ctx context.Context, id int64, evaluatedAt time.Time) error
	RecordRuleRun(ctx context.Context, run domain.RuleRun) error
}

type Executor interface {
	Execute(ctx context.Context, settings domain.Settings, rule domain.Rule) error
}

type ExecutorFunc func(context.Context, domain.Settings, domain.Rule) error

func (fn ExecutorFunc) Execute(ctx context.Context, settings domain.Settings, rule domain.Rule) error {
	return fn(ctx, settings, rule)
}

type Service struct {
	store    Store
	executor Executor
	now      func() time.Time
}

func New(store Store, executor Executor, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, executor: executor, now: now}
}

func (s *Service) Tick(ctx context.Context) error {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("load scheduler settings: %w", err)
	}
	locationName := strings.TrimSpace(settings.Timezone)
	if locationName == "" {
		locationName = "UTC"
	}
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return fmt.Errorf("load scheduler timezone: %w", err)
	}
	now := s.now().In(location)

	rules, err := s.store.ListRules(ctx)
	if err != nil {
		return fmt.Errorf("list rules: %w", err)
	}

	var collected []error
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if err := s.processRule(ctx, settings, rule, now, location); err != nil {
			collected = append(collected, fmt.Errorf("rule %d (%s): %w", rule.ID, rule.Name, err))
		}
	}
	return errors.Join(collected...)
}

func (s *Service) processRule(ctx context.Context, settings domain.Settings, rule domain.Rule, now time.Time, location *time.Location) error {
	expression, err := cronexpr.Parse(rule.Schedule)
	if err != nil {
		return s.recordError(ctx, rule, now, err)
	}

	windowStart := rule.UpdatedAt
	if rule.LastEvaluatedAt != nil {
		windowStart = *rule.LastEvaluatedAt
	}
	occurrences, err := expression.Between(windowStart.In(location), now, 512)
	if err != nil {
		return s.recordError(ctx, rule, now, err)
	}

	var collected []error
	for _, scheduledFor := range occurrences {
		execErr := s.executor.Execute(ctx, settings, rule)
		status := "success"
		message := "executed"
		if execErr != nil {
			status = "error"
			message = execErr.Error()
			collected = append(collected, execErr)
		}
		run := domain.RuleRun{
			RuleID:       rule.ID,
			RuleName:     rule.Name,
			Action:       rule.Action,
			TargetKind:   rule.TargetKind,
			TargetID:     rule.TargetID,
			TargetName:   rule.TargetName,
			ScheduledFor: scheduledFor.UTC(),
			ExecutedAt:   s.now().UTC(),
			Status:       status,
			Message:      message,
		}
		if err := s.store.RecordRuleRun(ctx, run); err != nil {
			collected = append(collected, err)
		}
	}

	if err := s.store.MarkRuleEvaluated(ctx, rule.ID, s.now().UTC()); err != nil {
		collected = append(collected, err)
	}
	return errors.Join(collected...)
}

func (s *Service) recordError(ctx context.Context, rule domain.Rule, now time.Time, reason error) error {
	run := domain.RuleRun{
		RuleID:       rule.ID,
		RuleName:     rule.Name,
		Action:       rule.Action,
		TargetKind:   rule.TargetKind,
		TargetID:     rule.TargetID,
		TargetName:   rule.TargetName,
		ScheduledFor: now.UTC(),
		ExecutedAt:   s.now().UTC(),
		Status:       "error",
		Message:      reason.Error(),
	}
	if err := s.store.RecordRuleRun(ctx, run); err != nil {
		return errors.Join(reason, err)
	}
	if err := s.store.MarkRuleEvaluated(ctx, rule.ID, s.now().UTC()); err != nil {
		return errors.Join(reason, err)
	}
	return reason
}
