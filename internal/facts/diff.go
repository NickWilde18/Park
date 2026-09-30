package facts

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Change is one field's change: its value before and after, as JSON. Before is null for a new pull request.
type Change struct {
	Old json.RawMessage `json:"old"`
	New json.RawMessage `json:"new"`
}

// fields is a PR as its JSON fields.
func fields(p *PR) (map[string]json.RawMessage, error) {
	if p == nil {
		return map[string]json.RawMessage{}, nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	err = json.Unmarshal(data, &out)
	return out, err
}

// Diff returns the fields that differ between the stored state, nil for none, and the fetched one. The same state
// gives no changes, so refreshing it again records nothing (F3).
func Diff(old, updated *PR) (map[string]Change, error) {
	before, err := fields(old)
	if err != nil {
		return nil, err
	}
	after, err := fields(updated)
	if err != nil {
		return nil, err
	}
	changes := map[string]Change{}
	for k, v := range after {
		was, ok := before[k]
		if !ok {
			was = json.RawMessage("null")
		}
		if !bytes.Equal(was, v) {
			changes[k] = Change{Old: was, New: v}
		}
	}
	return changes, nil
}

// Changed lists the names of the changed fields, sorted.
func Changed(changes map[string]Change) []string {
	names := make([]string, 0, len(changes))
	for k := range changes {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Replay rebuilds a pull request's state from its fact events, in sequence order (F1).
func Replay(events []map[string]Change) (PR, error) {
	state := map[string]json.RawMessage{}
	for _, changes := range events {
		for k, c := range changes {
			state[k] = c.New
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return PR{}, err
	}
	var p PR
	err = json.Unmarshal(data, &p)
	return p, err
}
