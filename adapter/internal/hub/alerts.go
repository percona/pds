package hub

import "time"

// AlertSource is what one environment last reported, with whether it is still
// reporting. Beat is the zero value until the environment has checked in.
type AlertSource struct {
	Environment Environment
	Seen        bool
	Beat        Heartbeat
}

// AlertSources returns every environment's connectivity and last heartbeat
// snapshot, sorted by name. The snapshot is shared, so callers read it and
// never change it.
func (s *Service) AlertSources() []AlertSource {
	environments := s.inv.List(s.now())
	out := make([]AlertSource, 0, len(environments))
	for _, c := range environments {
		beat, seen := s.inv.Snapshot(c.Name)
		out = append(out, AlertSource{Environment: c, Seen: seen, Beat: beat})
	}

	return out
}

// Now is the clock this service measures staleness against.
func (s *Service) Now() time.Time { return s.now() }
