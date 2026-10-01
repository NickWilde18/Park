package events

import "testing"

// A stream misses notices only when some after the last it saw have been forgotten: not when it has seen them all,
// nor when the next it needs is still kept. With none kept, it has missed any numbered after it.
func TestMissed(t *testing.T) {
	for _, c := range []struct {
		name                string
		after, oldest, last int64
		missed              bool
	}{
		{"seen every one", 9, 3, 9, false},
		{"the next one kept", 5, 6, 9, false},
		{"older ones forgotten, the next kept", 5, 4, 9, false},
		{"the next one forgotten", 5, 7, 9, true},
		{"none kept, none numbered after", 9, 0, 9, false},
		{"none kept, some numbered after", 5, 0, 9, true},
	} {
		if got := Missed(c.after, c.oldest, c.last); got != c.missed {
			t.Errorf("%s: Missed(%d, %d, %d) = %v", c.name, c.after, c.oldest, c.last, got)
		}
	}
}
