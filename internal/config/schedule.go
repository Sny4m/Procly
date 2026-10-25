package config

import (
	"errors"
	"fmt"
	"time"
)

// NewSchedule accepts the same timing formats in the dashboard and CLI.
func NewSchedule(name, command, at, every string, now time.Time) (Schedule, error) {
	s := Schedule{Name: name, Alias: command, Enabled: true}
	if (at == "") == (every == "") {
		return s, errors.New("choose a date or an interval, not both")
	}
	var err error
	if at != "" {
		s.At, err = time.Parse(time.RFC3339, at)
		if err != nil {
			s.At, err = time.ParseInLocation("2006-01-02 15:04", at, time.Local)
		}
		if err != nil {
			return s, fmt.Errorf("use YYYY-MM-DD HH:MM or a date with timezone: %w", err)
		}
		if !s.At.After(now) {
			return s, errors.New("scheduled time must be in the future")
		}
	} else {
		s.Every, err = time.ParseDuration(every)
		if err != nil {
			return s, fmt.Errorf("enter an interval such as 30m: %w", err)
		}
		s.Next = now.Add(s.Every)
	}
	return s, s.Validate()
}
