package slidingwindow

import (
	"runtime"
	"sync"
	"time"
)

type Counter struct {
	size time.Duration

	mu sync.Mutex

	curr Window
	prev Window

	syncInterval time.Duration
	syncStopCh   chan struct{}
}

// NewCounter creates a new counter, and returns a function to stop
// the possible sync behaviour within the current window.
func NewCounter(size time.Duration, newWindow NewWindow, syncInterval time.Duration) (*Counter, StopFunc) {
	currWin, currStop := newWindow()

	// The previous window is static (i.e. no add changes will happen within it),
	// so we always create it as an instance of LocalWindow.
	//
	// In this way, the whole limiter, despite containing two windows, now only
	// consumes at most one goroutine for the possible sync behaviour within
	// the current window.
	prevWin, _ := NewLocalWindow()

	c := &Counter{
		size:         size,
		curr:         currWin,
		prev:         prevWin,
		syncInterval: syncInterval,
	}

	if syncInterval > 0 {
		go c.Sync(syncInterval)

		runtime.SetFinalizer(c, func(counter *Counter) {
			close(counter.syncStopCh)
		})
	}

	return c, currStop
}

func (counter *Counter) Sync(interval time.Duration) {
	for {
		select {
		case <-time.After(interval):
		case <-counter.syncStopCh:
			return
		}

		counter.mu.Lock()
		counter.curr.Sync(time.Now())
		counter.mu.Unlock()
	}
}

// Size returns the time duration of one window size. Note that the size
// is defined to be read-only, if you need to change the size,
// create a new limiter with a new size instead.
func (counter *Counter) Size() time.Duration {
	return counter.size
}

// Increment is shorthand for AddN(time.Now(), 1).
func (counter *Counter) Increment() {
	counter.AddN(time.Now(), 1)
}

// AddN reports whether n events may happen at time now.
func (counter *Counter) AddN(now time.Time, n int64) {
	counter.mu.Lock()
	defer counter.mu.Unlock()

	counter.advance(now)

	// Trigger the possible sync behaviour.
	defer counter.curr.Sync(now)

	counter.curr.AddCount(n)
}

func (counter *Counter) Count(now time.Time) int64 {
	counter.mu.Lock()
	defer counter.mu.Unlock()

	counter.advance(now)

	elapsed := now.Sub(counter.curr.Start())
	weight := float64(counter.size-elapsed) / float64(counter.size)
	count := int64(weight*float64(counter.prev.Count())) + counter.curr.Count()

	return count
}

// advance updates the current/previous windows resulting from the passage of time.
func (counter *Counter) advance(now time.Time) {
	// Calculate the start boundary of the expected current-window.
	newCurrStart := now.Truncate(counter.size)

	diffSize := newCurrStart.Sub(counter.curr.Start()) / counter.size
	if diffSize >= 1 {
		// The current-window is at least one-window-size behind the expected one.

		newPrevCount := int64(0)
		if diffSize == 1 {
			// The new previous-window will overlap with the old current-window,
			// so it inherits the count.
			//
			// Note that the count here may be not accurate, since it is only a
			// SNAPSHOT of the current-window's count, which in itself tends to
			// be inaccurate due to the asynchronous nature of the sync behaviour.
			newPrevCount = counter.curr.Count()
		}
		counter.prev.Reset(newCurrStart.Add(-counter.size), newPrevCount)

		// The new current-window always has zero count.
		counter.curr.Reset(newCurrStart, 0)
	}
}
