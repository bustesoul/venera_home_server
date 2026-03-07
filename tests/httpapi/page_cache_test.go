package httpapi_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	httpapipkg "venera_home_server/httpapi"
)

func TestPrefetchOrderUsesForwardWindow(t *testing.T) {
	s := httpapipkg.NewForTests(0, nil)
	got := s.PrefetchOrder(101, 4)
	if len(got) != 12 {
		t.Fatalf("expected 12 prefetched pages, got %d", len(got))
	}
	if got[0] != 5 || got[len(got)-1] != 16 {
		t.Fatalf("unexpected prefetch order bounds: %#v", got)
	}
	for i, pageIndex := range got {
		if pageIndex != 5+i {
			t.Fatalf("unexpected prefetch order at %d: got %d", i, pageIndex)
		}
	}
}

func TestPageFlightDedupesConcurrentCalls(t *testing.T) {
	s := httpapipkg.NewForTests(0, nil)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var runs int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err, _ := s.DoPageFlight("page-key", func() (bool, error) {
				if atomic.AddInt32(&runs, 1) == 1 {
					started <- struct{}{}
				}
				<-release
				return true, nil
			})
			if err != nil {
				t.Errorf("DoPageFlight returned error: %v", err)
			}
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first flight to start")
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("expected exactly 1 underlying run, got %d", got)
	}
}

func TestPrefetchThrottleKeyCoalescesAdjacentWindows(t *testing.T) {
	base := httpapipkg.PrefetchThrottleKey("chapter-1", 16)
	for _, start := range []int{16, 17, 18, 19} {
		if got := httpapipkg.PrefetchThrottleKey("chapter-1", start); got != base {
			t.Fatalf("expected window start %d to share throttle key %s, got %s", start, base, got)
		}
	}
	if got := httpapipkg.PrefetchThrottleKey("chapter-1", 20); got == base {
		t.Fatalf("expected next coalesced window to use a different key, got %s", got)
	}
}
