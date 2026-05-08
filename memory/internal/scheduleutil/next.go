// Package scheduleutil computes next run times for scheduled jobs (interval or cron).
package scheduleutil

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// NextRunTime returns the time of the next run strictly after the completed run (`from` is UTC).
func NextRunTime(scheduleKind, cronExpr, timeZone string, intervalSec int32, from time.Time) time.Time {
	from = from.UTC()
	switch scheduleKind {
	case "cron":
		if cronExpr == "" {
			return from.Add(24 * time.Hour)
		}
		loc := time.UTC
		if timeZone != "" {
			if l, err := time.LoadLocation(timeZone); err == nil {
				loc = l
			}
		}
		p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := p.Parse(cronExpr)
		if err != nil {
			return from.Add(24 * time.Hour)
		}
		return sched.Next(from.In(loc)).UTC()
	default:
		if intervalSec > 0 {
			return from.Add(time.Duration(intervalSec) * time.Second)
		}
		return from
	}
}

// ValidateCron checks a 5-field cron expression.
func ValidateCron(expr string) error {
	if expr == "" {
		return fmt.Errorf("cronExpression is required for scheduleKind=cron")
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err := p.Parse(expr)
	return err
}
