package domain

import (
	"fmt"
	"strings"
	"time"
)

type Action string

const (
	ActionPause  Action = "pause"
	ActionResume Action = "resume"
)

func ParseAction(value string) (Action, error) {
	action := Action(strings.ToLower(strings.TrimSpace(value)))
	switch action {
	case ActionPause, ActionResume:
		return action, nil
	default:
		return "", fmt.Errorf("unsupported action %q", value)
	}
}

func (a Action) PausedValue() bool {
	return a == ActionPause
}

type TargetKind string

const (
	TargetGlobal TargetKind = "global"
	TargetDevice TargetKind = "device"
	TargetFolder TargetKind = "folder"
)

func ParseTargetKind(value string) (TargetKind, error) {
	kind := TargetKind(strings.ToLower(strings.TrimSpace(value)))
	switch kind {
	case TargetGlobal, TargetDevice, TargetFolder:
		return kind, nil
	default:
		return "", fmt.Errorf("unsupported target kind %q", value)
	}
}

type Settings struct {
	SyncthingURL    string
	SyncthingAPIKey string
	Timezone        string
	UpdatedAt       time.Time
}

type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string
	UpdatedAt    time.Time
}

type Session struct {
	TokenHash string
	Username  string
	ExpiresAt time.Time
	CreatedAt time.Time
}

type Rule struct {
	ID              int64
	Name            string
	Schedule        string
	Action          Action
	TargetKind      TargetKind
	TargetID        string
	TargetName      string
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastEvaluatedAt *time.Time
}

func (r Rule) ValidateBasic() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("rule name is required")
	}
	if strings.TrimSpace(r.Schedule) == "" {
		return fmt.Errorf("schedule is required")
	}
	if _, err := ParseAction(string(r.Action)); err != nil {
		return err
	}
	if _, err := ParseTargetKind(string(r.TargetKind)); err != nil {
		return err
	}
	if r.TargetKind != TargetGlobal && strings.TrimSpace(r.TargetID) == "" {
		return fmt.Errorf("target id is required for %s rules", r.TargetKind)
	}
	return nil
}

type RuleRun struct {
	ID           int64
	RuleID       int64
	RuleName     string
	Action       Action
	TargetKind   TargetKind
	TargetID     string
	TargetName   string
	ScheduledFor time.Time
	ExecutedAt   time.Time
	Status       string
	Message      string
}

type Device struct {
	ID     string
	Name   string
	Paused bool
}

type Folder struct {
	ID     string
	Label  string
	Paused bool
}
