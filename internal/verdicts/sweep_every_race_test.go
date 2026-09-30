//go:build race

package verdicts

// sweepEvery runs one chunk in fifty of the sweep: under the race detector, the whole of it takes minutes.
const sweepEvery = 50
