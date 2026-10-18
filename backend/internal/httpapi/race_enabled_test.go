//go:build race

package httpapi

// raceInstrumentation is true when the test binary runs with the race
// detector. Wall-clock ceilings measure the product, not the instrumentation,
// so the large index contract only enforces its budget in the dedicated
// non-race job.
const raceInstrumentation = true
