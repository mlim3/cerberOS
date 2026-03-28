package lifecycle

import (
	"sync"
	"testing"
	"time"
)

// baseTime is a fixed reference point used across all tests so results are deterministic.
var baseTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func newDetector(t *testing.T, interval time.Duration, maxMissed int, onCrash func(string)) *CrashDetector {
	t.Helper()
	return NewCrashDetector(HeartbeatConfig{Interval: interval, MaxMissed: maxMissed}, onCrash)
}

// watchAt seeds an agent watch with a controlled lastSeen timestamp.
// Used in tests to bypass time.Now() in Watch so tick() comparisons are deterministic.
func watchAt(d *CrashDetector, agentID string, lastSeen time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.watches[agentID] = &agentWatch{lastSeen: lastSeen}
}

// TestDefaultsApplied verifies that zero-value config fields get sensible defaults.
func TestDefaultsApplied(t *testing.T) {
	d := NewCrashDetector(HeartbeatConfig{}, func(string) {})
	if d.cfg.Interval != 5*time.Second {
		t.Errorf("default Interval: want 5s, got %v", d.cfg.Interval)
	}
	if d.cfg.MaxMissed != 3 {
		t.Errorf("default MaxMissed: want 3, got %d", d.cfg.MaxMissed)
	}
}

// TestWatchedCount confirms Watch and Unwatch update the count correctly.
func TestWatchedCount(t *testing.T) {
	d := newDetector(t, time.Second, 3, func(string) {})

	if n := d.Watched(); n != 0 {
		t.Fatalf("initial Watched: want 0, got %d", n)
	}
	d.Watch("a1")
	d.Watch("a2")
	if n := d.Watched(); n != 2 {
		t.Errorf("after Watch×2: want 2, got %d", n)
	}
	d.Unwatch("a1")
	if n := d.Watched(); n != 1 {
		t.Errorf("after Unwatch: want 1, got %d", n)
	}
}

// TestNoHeartbeatBeforeMaxMissed confirms the callback is NOT fired before
// MaxMissed intervals have elapsed without a heartbeat.
func TestNoHeartbeatBeforeMaxMissed(t *testing.T) {
	var crashed []string
	d := newDetector(t, time.Second, 3, func(id string) { crashed = append(crashed, id) })

	watchAt(d, "a1", baseTime)
	// Only two ticks — below the MaxMissed=3 threshold.
	d.tick(baseTime.Add(1 * time.Second))
	d.tick(baseTime.Add(2 * time.Second))

	if len(crashed) != 0 {
		t.Errorf("expected no crash before MaxMissed, got: %v", crashed)
	}
}

// TestCrashAfterMaxMissed confirms the callback fires exactly once after
// MaxMissed consecutive missed intervals.
func TestCrashAfterMaxMissed(t *testing.T) {
	var crashed []string
	d := newDetector(t, time.Second, 3, func(id string) { crashed = append(crashed, id) })

	watchAt(d, "a1", baseTime)
	// Tick three times — each tick is 1 second after baseTime so the elapsed time
	// exceeds the 1s Interval. This produces 3 missed ticks = MaxMissed.
	d.tick(baseTime.Add(1 * time.Second))
	d.tick(baseTime.Add(2 * time.Second))
	d.tick(baseTime.Add(3 * time.Second))

	if len(crashed) != 1 || crashed[0] != "a1" {
		t.Errorf("expected one crash for a1, got: %v", crashed)
	}
}

// TestNoDuplicateCrashCallback confirms onCrash fires at most once per agent.
func TestNoDuplicateCrashCallback(t *testing.T) {
	var mu sync.Mutex
	var count int
	d := newDetector(t, time.Second, 3, func(string) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	watchAt(d, "a1", baseTime)
	for i := 1; i <= 10; i++ {
		d.tick(baseTime.Add(time.Duration(i) * time.Second))
	}

	mu.Lock()
	got := count
	mu.Unlock()
	if got != 1 {
		t.Errorf("onCrash invocations: want 1, got %d", got)
	}
}

// TestHeartbeatResetsMissedCounter confirms RecordHeartbeat resets the missed
// counter so a live agent is never falsely declared crashed.
func TestHeartbeatResetsMissedCounter(t *testing.T) {
	var crashed []string
	d := newDetector(t, time.Second, 3, func(id string) { crashed = append(crashed, id) })

	watchAt(d, "a1", baseTime)

	// Two missed ticks — just below MaxMissed=3.
	d.tick(baseTime.Add(1 * time.Second))
	d.tick(baseTime.Add(2 * time.Second))

	// Agent sends a heartbeat. The missed counter and lastSeen reset.
	d.RecordHeartbeat("a1")

	// Two more ticks relative to the now-reset lastSeen.
	// RecordHeartbeat sets lastSeen = time.Now() (real time), so subsequent
	// ticks must be far enough in the future to count as missed.
	resetAt := time.Now()
	d.tick(resetAt.Add(1 * time.Second))
	d.tick(resetAt.Add(2 * time.Second))

	if len(crashed) != 0 {
		t.Errorf("expected no crash after heartbeat reset, got: %v", crashed)
	}
}

// TestUnwatchPreventsCallback confirms that an Unwatched agent is never reported
// as crashed even after enough missed ticks would have triggered it.
func TestUnwatchPreventsCallback(t *testing.T) {
	var crashed []string
	d := newDetector(t, time.Second, 3, func(id string) { crashed = append(crashed, id) })

	watchAt(d, "a1", baseTime)
	d.tick(baseTime.Add(1 * time.Second))
	d.tick(baseTime.Add(2 * time.Second))

	// Intentional shutdown — remove from tracking before the third tick.
	d.Unwatch("a1")
	d.tick(baseTime.Add(3 * time.Second))

	if len(crashed) != 0 {
		t.Errorf("expected no crash for unwatched agent, got: %v", crashed)
	}
}

// TestWatchResetsCounters confirms that calling Watch on an already-watched agent
// resets its missed counter so a previously-suspect agent gets a clean slate.
func TestWatchResetsCounters(t *testing.T) {
	var crashed []string
	d := newDetector(t, time.Second, 3, func(id string) { crashed = append(crashed, id) })

	watchAt(d, "a1", baseTime)
	d.tick(baseTime.Add(1 * time.Second))
	d.tick(baseTime.Add(2 * time.Second))
	// Two missed ticks recorded. Re-watch resets to a fresh lastSeen.
	d.Watch("a1")

	// Watch stamps lastSeen = time.Now(), so ticks must be future-relative.
	freshBase := time.Now()
	d.tick(freshBase.Add(1 * time.Second))
	d.tick(freshBase.Add(2 * time.Second))

	if len(crashed) != 0 {
		t.Errorf("expected no crash after re-watch, got: %v", crashed)
	}
}

// TestRecordHeartbeatUnknownAgent confirms RecordHeartbeat is a no-op for
// agents that are not currently watched (e.g. stale heartbeats after Unwatch).
func TestRecordHeartbeatUnknownAgent(t *testing.T) {
	d := newDetector(t, time.Second, 3, func(string) {})
	// Must not panic.
	d.RecordHeartbeat("ghost")
}

// TestMultipleAgents confirms the detector handles multiple agents independently.
func TestMultipleAgents(t *testing.T) {
	var mu sync.Mutex
	crashed := map[string]int{}
	d := newDetector(t, time.Second, 2, func(id string) {
		mu.Lock()
		crashed[id]++
		mu.Unlock()
	})

	watchAt(d, "a1", baseTime) // will miss heartbeats
	watchAt(d, "a2", baseTime) // will keep sending heartbeats

	d.tick(baseTime.Add(1 * time.Second))
	d.RecordHeartbeat("a2") // a2 is alive; resets lastSeen to time.Now()
	d.tick(baseTime.Add(2 * time.Second))
	d.RecordHeartbeat("a2")
	d.tick(baseTime.Add(3 * time.Second))

	mu.Lock()
	a1, a2 := crashed["a1"], crashed["a2"]
	mu.Unlock()

	if a1 != 1 {
		t.Errorf("a1 crash count: want 1, got %d", a1)
	}
	if a2 != 0 {
		t.Errorf("a2 crash count: want 0, got %d", a2)
	}
}
