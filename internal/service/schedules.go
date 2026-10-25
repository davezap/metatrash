package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// Schedules (0.57.0). An action with "every" runs on a schedule in plain
// words instead of when files arrive:
//
//	hour, hour :30           every hour, on the hour or at 30 past
//	day 06:00                every day
//	weekday 08:00            Monday to Friday
//	monday 08:00             one day of the week
//	month 1 09:00            one day of the month (1 to 28)
//
// in the space's own time zone (Your account → Spaces), or the server's
// agents.timezone for a space without one. metatrash_action_schedules holds
// each action's next run; the runner keeps it in step with the folders'
// settings and queues a job when the time comes round.

type schedule struct {
	// kind is hour, day, weekday, week or month.
	kind         string
	hour, minute int
	weekday      time.Weekday
	monthDay     int
}

var weekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

const everyHelp = "every is a schedule in plain words: hour, hour :30, day 06:00, weekday 08:00 (Monday to Friday), monday 08:00 or month 1 09:00 (days 1 to 28), in the space's time zone"

// parseEvery reads an "every" schedule.
func parseEvery(text string) (schedule, error) {
	bad := fmt.Errorf("%s (got %q).", everyHelp, text)
	fields := strings.Fields(strings.ToLower(text))
	clock := func(s string) (int, int, bool) {
		h, m, ok := strings.Cut(s, ":")
		hour, err1 := strconv.Atoi(h)
		minute, err2 := strconv.Atoi(m)
		if !ok || err1 != nil || err2 != nil || len(m) != 2 || len(h) > 2 || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return 0, 0, false
		}
		return hour, minute, true
	}
	if len(fields) == 0 || len(text) > 40 {
		return schedule{}, bad
	}
	s := schedule{kind: fields[0]}
	ok := true
	switch {
	case fields[0] == "hour" && len(fields) == 1:
	case fields[0] == "hour" && len(fields) == 2 && strings.HasPrefix(fields[1], ":"):
		_, s.minute, ok = clock("0" + fields[1])
	case (fields[0] == "day" || fields[0] == "weekday") && len(fields) == 2:
		s.hour, s.minute, ok = clock(fields[1])
	case fields[0] == "month" && len(fields) == 3:
		day, err := strconv.Atoi(fields[1])
		s.monthDay = day
		s.hour, s.minute, ok = clock(fields[2])
		ok = ok && err == nil && day >= 1 && day <= 28
	default:
		day, isDay := weekdays[fields[0]]
		if !isDay || len(fields) != 2 {
			return schedule{}, bad
		}
		s.kind, s.weekday = "week", day
		s.hour, s.minute, ok = clock(fields[1])
	}
	if !ok {
		return schedule{}, bad
	}
	return s, nil
}

// next is the first run strictly after after, in zone.
func (s schedule) next(after time.Time, zone *time.Location) time.Time {
	local := after.In(zone)
	if s.kind == "hour" {
		t := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), s.minute, 0, 0, zone)
		for !t.After(after) {
			t = t.Add(time.Hour)
		}
		return t
	}
	for day := 0; day <= 62; day++ {
		t := time.Date(local.Year(), local.Month(), local.Day()+day, s.hour, s.minute, 0, 0, zone)
		if !t.After(after) {
			continue
		}
		switch s.kind {
		case "day":
			return t
		case "weekday":
			if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday {
				return t
			}
		case "week":
			if t.Weekday() == s.weekday {
				return t
			}
		case "month":
			if t.Day() == s.monthDay {
				return t
			}
		}
	}
	return after.Add(24 * time.Hour) // not reached
}

// actionSchedule is one row of metatrash_action_schedules.
type actionSchedule struct {
	SpaceID, Folder, Action, Every string
	NextRun, LastRun               time.Time
}

const scheduleColumns = "space_id, folder, action, every, next_run_at, last_run_at"

func (s dbJobStore) scanSchedules(ctx context.Context, query string, args ...any) ([]actionSchedule, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+scheduleColumns+" FROM metatrash_action_schedules "+query, args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read schedules")
	}
	defer rows.Close()
	var out []actionSchedule
	for rows.Next() {
		var a actionSchedule
		var next int64
		var last sql.NullInt64
		if err := rows.Scan(&a.SpaceID, &a.Folder, &a.Action, &a.Every, &next, &last); err != nil {
			return nil, fmt.Errorf("cannot read schedules")
		}
		a.NextRun = time.Unix(next, 0).UTC()
		if last.Valid {
			a.LastRun = time.Unix(last.Int64, 0).UTC()
		}
		out = append(out, a)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read schedules")
	}
	return out, nil
}

func (s dbJobStore) schedules(ctx context.Context, spaceID string) ([]actionSchedule, error) {
	return s.scanSchedules(ctx, "WHERE space_id = ? LIMIT 1000", spaceID)
}

func (s dbJobStore) dueSchedules(ctx context.Context, now time.Time, limit int) ([]actionSchedule, error) {
	return s.scanSchedules(ctx, "WHERE next_run_at <= ? ORDER BY next_run_at LIMIT ?", now.Unix(), limit)
}

func (s dbJobStore) addSchedule(ctx context.Context, a actionSchedule) error {
	if _, err := s.db.ExecContext(ctx, "INSERT INTO metatrash_action_schedules ("+scheduleColumns+") VALUES (?, ?, ?, ?, ?, ?)", a.SpaceID, a.Folder, a.Action, a.Every, a.NextRun.Unix(), unixOrNull(a.LastRun)); err != nil {
		return fmt.Errorf("cannot save the schedule")
	}
	return nil
}

func (s dbJobStore) setSchedule(ctx context.Context, a actionSchedule) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE metatrash_action_schedules SET every = ?, next_run_at = ? WHERE space_id = ? AND folder = ? AND action = ?", a.Every, a.NextRun.Unix(), a.SpaceID, a.Folder, a.Action); err != nil {
		return fmt.Errorf("cannot save the schedule")
	}
	return nil
}

func (s dbJobStore) removeSchedule(ctx context.Context, a actionSchedule) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM metatrash_action_schedules WHERE space_id = ? AND folder = ? AND action = ?", a.SpaceID, a.Folder, a.Action); err != nil {
		return fmt.Errorf("cannot remove the schedule")
	}
	return nil
}

func (s dbJobStore) advanceSchedule(ctx context.Context, a actionSchedule, next, ran time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE metatrash_action_schedules SET next_run_at = ?, last_run_at = ? WHERE space_id = ? AND folder = ? AND action = ? AND next_run_at = ?", next.Unix(), ran.Unix(), a.SpaceID, a.Folder, a.Action, a.NextRun.Unix())
	if err != nil {
		return false, fmt.Errorf("cannot advance the schedule")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("cannot advance the schedule")
	}
	return n == 1, nil
}

// zoneOf is a space's time zone: its own, or the server's agents.timezone.
func (a *agentRunner) zoneOf(ctx context.Context, spaceID string) *time.Location {
	name := ""
	if a.spaceZone != nil {
		name = a.spaceZone(ctx, spaceID)
	}
	if name != "" {
		if zone, err := time.LoadLocation(name); err == nil {
			return zone
		}
	}
	return a.settings.zone
}

// dbSpaceZone reads a space's own time zone; "" when it has none.
func (s *Service) dbSpaceZone(ctx context.Context, spaceID string) string {
	if s.ownedDB == nil {
		return ""
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var zone sql.NullString
	if err := s.ownedDB.db.QueryRowContext(lookup, "SELECT timezone FROM metatrash_spaces WHERE space_id = ?", spaceID).Scan(&zone); err != nil {
		return ""
	}
	return zone.String
}

// scheduledActions are a space's actions with "every", by folder and name.
func scheduledActions(ctx context.Context, r *repository) (map[[2]string]folderAction, error) {
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[[2]string]folderAction{}
	for p, f := range files {
		if !isFolderConfig(p) {
			continue
		}
		c, err := r.cachedConfig(ctx, f.Blob)
		if err != nil {
			continue
		}
		for _, action := range c.Actions {
			if action.Every != "" {
				out[[2]string{configFolder(p), action.Name}] = action
			}
		}
	}
	return out, nil
}

// syncSchedules brings a space's rows in step with its actions: new
// schedules get their first run, changed ones a new next run (also when
// rezone is set, after the space's time zone changed), and removed ones go.
func (a *agentRunner) syncSchedules(ctx context.Context, r *repository, rezone bool) error {
	want, err := scheduledActions(ctx, r)
	if err != nil {
		return err
	}
	have, err := a.store.schedules(ctx, r.id)
	if err != nil {
		return err
	}
	zone, now := a.zoneOf(ctx, r.id), a.clock()
	for _, row := range have {
		action, ok := want[[2]string{row.Folder, row.Action}]
		delete(want, [2]string{row.Folder, row.Action})
		if !ok {
			if err := a.store.removeSchedule(ctx, row); err != nil {
				return err
			}
			continue
		}
		if action.Every == row.Every && !rezone {
			continue
		}
		s, err := parseEvery(action.Every)
		if err != nil {
			continue
		}
		row.Every, row.NextRun = action.Every, s.next(now, zone)
		if err := a.store.setSchedule(ctx, row); err != nil {
			return err
		}
	}
	for key, action := range want {
		s, err := parseEvery(action.Every)
		if err != nil {
			continue
		}
		if err := a.store.addSchedule(ctx, actionSchedule{SpaceID: r.id, Folder: key[0], Action: key[1], Every: action.Every, NextRun: s.next(now, zone)}); err != nil {
			return err
		}
	}
	return nil
}

// syncAllSchedules syncs every loaded space, as on start.
func (a *agentRunner) syncAllSchedules() {
	a.s.ownedMu.RLock()
	repos := make([]*repository, 0, len(a.s.ownedRepos))
	for _, r := range a.s.ownedRepos {
		repos = append(repos, r)
	}
	a.s.ownedMu.RUnlock()
	for _, r := range repos {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		if err := a.syncSchedules(ctx, r, false); err != nil {
			log.Printf("agent runner: space %s schedules: %v", r.id, err)
		}
		cancel()
	}
}

// spaceZoneChanged moves a space's next runs to its new time zone.
func (s *Service) spaceZoneChanged(spaceID string) {
	a := s.agents.Load()
	r := s.ownedRepository(spaceID)
	if a == nil || r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if err := a.syncSchedules(ctx, r, true); err != nil {
		log.Printf("agent runner: space %s schedules: %v", spaceID, err)
	}
}

// runSchedules queues a job for each schedule that came round. A run missed
// while the service was down happens once; the next is counted from now.
func (a *agentRunner) runSchedules() {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	now := a.clock()
	due, err := a.store.dueSchedules(ctx, now, 50)
	if err != nil {
		log.Printf("agent runner: %v", err)
		return
	}
	queued := false
	for _, row := range due {
		r := a.s.ownedRepository(row.SpaceID)
		if r == nil {
			continue
		}
		actions, err := scheduledActions(ctx, r)
		if err != nil {
			log.Printf("agent runner: space %s: %v", r.id, err)
			continue
		}
		action, ok := actions[[2]string{row.Folder, row.Action}]
		s, parseErr := parseEvery(action.Every)
		if !ok || parseErr != nil {
			if err := a.store.removeSchedule(ctx, row); err != nil {
				log.Printf("agent runner: %v", err)
			}
			continue
		}
		won, err := a.store.advanceSchedule(ctx, row, s.next(now, a.zoneOf(ctx, r.id)), now)
		if err != nil || !won {
			if err != nil {
				log.Printf("agent runner: %v", err)
			}
			continue
		}
		head, err := r.head(ctx)
		if err != nil {
			log.Printf("agent runner: space %s: %v", r.id, err)
			continue
		}
		contributor := ""
		if action.Member != "" {
			contributor = a.s.userIDOf(ctx, action.Member)
		}
		ok, err = a.queue(ctx, r.id, actionTrigger{Folder: row.Folder, Action: row.Action}, head, contributor)
		if err != nil {
			log.Printf("agent runner: %v", err)
			continue
		}
		queued = queued || ok
	}
	if queued {
		a.poke()
	}
}
