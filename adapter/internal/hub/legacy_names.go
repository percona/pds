package hub

import "encoding/json"

// Documents written before an environment was called one name it cluster: the
// heartbeats a stored inventory holds, the join grants in the token store, and
// what an agent that has not been upgraded still posts. For one release both
// spellings decode, and only environment is written. Removing this file ends
// the window.

// UnmarshalJSON reads a heartbeat that names its environment cluster.
func (h *Heartbeat) UnmarshalJSON(data []byte) error {
	type plain Heartbeat
	aux := struct {
		*plain
		LegacyName string `json:"cluster"`
	}{plain: (*plain)(h)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if h.Environment == "" {
		h.Environment = aux.LegacyName
	}

	return nil
}

// UnmarshalJSON reads a grant that names its environment cluster.
func (g *Grant) UnmarshalJSON(data []byte) error {
	type plain Grant
	aux := struct {
		*plain
		LegacyName string `json:"cluster"`
	}{plain: (*plain)(g)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if g.Environment == "" {
		g.Environment = aux.LegacyName
	}

	return nil
}
