package main

import (
	"testing"
	"time"
)

// ── nil-receiver guards ───────────────────────────────────────────────────────
// All scheduling methods must be no-ops when sl is nil (NATS unavailable).

func TestSessionLog_NilReceiver_CreateScheduledJob(t *testing.T) {
	var sl *SessionLog
	err := sl.CreateScheduledJob(CreateScheduledJobParams{
		Name:            "daily",
		RawInput:        "do thing",
		ScheduleKind:    "interval",
		IntervalSeconds: 3600,
		NextRunAt:       time.Now().UTC().Add(time.Hour),
		UserContextID:   "user-1",
	})
	if err != nil {
		t.Errorf("nil receiver CreateScheduledJob: want nil error, got %v", err)
	}
}

func TestSessionLog_NilReceiver_ListScheduledJobs(t *testing.T) {
	var sl *SessionLog
	records, err := sl.ListScheduledJobs("user-1")
	if err != nil {
		t.Errorf("nil receiver ListScheduledJobs: want nil error, got %v", err)
	}
	if records != nil {
		t.Errorf("nil receiver ListScheduledJobs: want nil records, got %v", records)
	}
}

func TestSessionLog_NilReceiver_CancelScheduledJob(t *testing.T) {
	var sl *SessionLog
	err := sl.CancelScheduledJob("job-id-1", "user-1")
	if err != nil {
		t.Errorf("nil receiver CancelScheduledJob: want nil error, got %v", err)
	}
}

// ── CreateScheduledJobParams defaults ────────────────────────────────────────

func TestCreateScheduledJobParams_CronKind(t *testing.T) {
	// Verify the params struct is populated correctly for a cron schedule.
	p := CreateScheduledJobParams{
		Name:           "weekly_report",
		RawInput:       "Summarise the week.",
		ScheduleKind:   "cron",
		CronExpression: "0 9 * * 1",
		NextRunAt:      time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC),
		UserContextID:  "user-abc",
	}
	if p.ScheduleKind != "cron" {
		t.Errorf("schedule kind: want %q, got %q", "cron", p.ScheduleKind)
	}
	if p.IntervalSeconds != 0 {
		t.Errorf("cron kind must have zero IntervalSeconds, got %d", p.IntervalSeconds)
	}
	if p.CronExpression == "" {
		t.Error("cron expression must be set for cron schedule kind")
	}
}

func TestCreateScheduledJobParams_IntervalKind(t *testing.T) {
	p := CreateScheduledJobParams{
		Name:            "hourly_check",
		RawInput:        "Check system status.",
		ScheduleKind:    "interval",
		IntervalSeconds: 3600,
		NextRunAt:       time.Now().UTC().Add(time.Hour),
		UserContextID:   "user-xyz",
	}
	if p.ScheduleKind != "interval" {
		t.Errorf("schedule kind: want %q, got %q", "interval", p.ScheduleKind)
	}
	if p.IntervalSeconds <= 0 {
		t.Error("interval kind must have positive IntervalSeconds")
	}
	if p.CronExpression != "" {
		t.Errorf("interval kind must have empty CronExpression, got %q", p.CronExpression)
	}
}

func TestCreateScheduledJobParams_RequiredSkillDomains(t *testing.T) {
	p := CreateScheduledJobParams{
		Name:                 "flight_watcher",
		RawInput:             "Check Google Flights for deals to Japan under $600.",
		ScheduleKind:         "interval",
		IntervalSeconds:      180,
		NextRunAt:            time.Now().UTC().Add(3 * time.Minute),
		UserContextID:        "user-1",
		RequiredSkillDomains: []string{"google_workspace", "web"},
	}
	if len(p.RequiredSkillDomains) != 2 {
		t.Errorf("want 2 skill domains, got %d", len(p.RequiredSkillDomains))
	}
	if p.RequiredSkillDomains[0] != "google_workspace" {
		t.Errorf("want %q, got %q", "google_workspace", p.RequiredSkillDomains[0])
	}
}

func TestCreateScheduledJobParams_NilRequiredSkillDomains(t *testing.T) {
	// nil domains is valid — dispatcher treats nil as unrestricted scope.
	p := CreateScheduledJobParams{
		Name:            "no_cred_job",
		RawInput:        "Log a note.",
		ScheduleKind:    "interval",
		IntervalSeconds: 3600,
		NextRunAt:       time.Now().UTC().Add(time.Hour),
		UserContextID:   "user-1",
	}
	if p.RequiredSkillDomains != nil {
		t.Errorf("unset RequiredSkillDomains must be nil, got %v", p.RequiredSkillDomains)
	}
}
