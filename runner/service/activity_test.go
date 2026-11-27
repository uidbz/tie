package service

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/images"
)

func TestShouldStopForInactivity(t *testing.T) {
	now := time.Now()
	old := now.Add(-90 * time.Second)

	if shouldStopForInactivity(now, old, 0, 0) {
		t.Error("timeout 0 must disable the sweep")
	}
	if shouldStopForInactivity(now, old, -5, 0) {
		t.Error("negative timeout must disable the sweep")
	}
	if shouldStopForInactivity(now, old, 60, 2) {
		t.Error("open tunnels count as activity")
	}
	// Exactly on the boundary gets one more sweep (strictly past only).
	if shouldStopForInactivity(now, now.Add(-60*time.Second), 60, 0) {
		t.Error("boundary must not reap")
	}
	if !shouldStopForInactivity(now, old, 60, 0) {
		t.Error("idle past window must reap")
	}
}

func idleTestManager(t *testing.T, img images.Image) *Manager {
	t.Helper()
	bin, _, _ := writeFakeCLI(t, "true 0")
	return New(Config{
		Images:            images.New(nil, img),
		Bin:               bin,
		ExtDir:            t.TempDir(),
		Ports:             NewPortPool(2000, 2002),
		ReadinessDelay:    5 * time.Millisecond,
		ReadinessInterval: 5 * time.Millisecond,
		ReadinessTimeout:  50 * time.Millisecond,
	})
}

func TestSweepInactivityStopsIdleService(t *testing.T) {
	m := idleTestManager(t, images.Image{
		Name: "svc:v1", IsService: true, ServiceInternalPort: 8080, ServiceInactivityTimeout: 60,
	})
	if _, err := m.StartService("svc:v1"); err != nil {
		t.Fatal(err)
	}
	// Back-date the clock five minutes behind the sweep's "now".
	backdated := time.Now().Add(-5 * time.Minute)
	m.actMu.Lock()
	m.lastAccess["svc:v1"] = backdated
	m.actMu.Unlock()

	m.sweepInactivity(backdated.Add(90 * time.Second))
	if _, ok := m.RunningPort("svc:v1"); ok {
		t.Fatal("idle service must be stopped by the sweep")
	}

	// A service with no timeout configured is never reaped.
	m2 := idleTestManager(t, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080})
	if _, err := m2.StartService("svc:v1"); err != nil {
		t.Fatal(err)
	}
	m2.actMu.Lock()
	m2.lastAccess["svc:v1"] = time.Now().Add(-24 * time.Hour)
	m2.actMu.Unlock()
	m2.sweepInactivity(time.Now())
	if _, ok := m2.RunningPort("svc:v1"); !ok {
		t.Error("service without a timeout must never be reaped")
	}
}

func TestSweepLeavesTunnelsAlone(t *testing.T) {
	m := idleTestManager(t, images.Image{
		Name: "svc:v1", IsService: true, ServiceInternalPort: 8080, ServiceInactivityTimeout: 60,
	})
	if _, err := m.StartService("svc:v1"); err != nil {
		t.Fatal(err)
	}
	m.actMu.Lock()
	m.lastAccess["svc:v1"] = time.Now().Add(-5 * time.Minute)
	m.actMu.Unlock()

	m.TunnelOpen("svc:v1")
	m.actMu.Lock() // an open tunnel also re-stamps; back-date again to test ONLY the tunnel rule
	m.lastAccess["svc:v1"] = time.Now().Add(-5 * time.Minute)
	m.actMu.Unlock()

	m.sweepInactivity(time.Now())
	if _, ok := m.RunningPort("svc:v1"); !ok {
		t.Fatal("a service with an open WebSocket tunnel must not be reaped")
	}

	m.TunnelClosed("svc:v1") // closes AND re-stamps the clock
	m.actMu.Lock()
	m.lastAccess["svc:v1"] = time.Now().Add(-5 * time.Minute)
	m.actMu.Unlock()
	m.sweepInactivity(time.Now())
	if _, ok := m.RunningPort("svc:v1"); ok {
		t.Fatal("after the last tunnel closes the idle service must be reaped")
	}
}

func TestWakeOnAccess(t *testing.T) {
	// Two OS-known-free ports make the pool deterministic: the first is
	// pre-bound (so the pool allocates the second), and a fake backend
	// listener starts on the allocated port shortly after.
	probe, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	base := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	blocked, err := net.Listen("tcp", fmt.Sprintf(":%d", base))
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()

	// Backend appears ~10ms in, exercising the bounded readiness wait.
	go func() {
		time.Sleep(10 * time.Millisecond)
		if ln, err := net.Listen("tcp", fmt.Sprintf(":%d", base+1)); err == nil {
			defer ln.Close()
			time.Sleep(2 * time.Second)
		}
	}()

	oldTimeout, oldInterval := startOnAccessReadyTimeout, startOnAccessReadyInterval
	startOnAccessReadyTimeout, startOnAccessReadyInterval = 2*time.Second, time.Millisecond
	defer func() {
		startOnAccessReadyTimeout, startOnAccessReadyInterval = oldTimeout, oldInterval
	}()

	bin, argsLog, _ := writeFakeCLI(t, "true 0")
	m := New(Config{
		Images: images.New(nil, images.Image{
			Name: "svc:v1", IsService: true, ServiceInternalPort: 8080, ServiceStartOnAccess: true,
		}),
		Bin:               bin,
		Registry:          "registry.example/tie",
		ExtDir:            t.TempDir(),
		Ports:             NewPortPool(base, base+1),
		ReadinessDelay:    5 * time.Millisecond,
		ReadinessInterval: 5 * time.Millisecond,
		ReadinessTimeout:  50 * time.Millisecond,
	})

	port, ok := m.WakeOnAccess("svc:v1")
	if !ok || port != base+1 {
		t.Fatalf("wake = %d %v, want %d true", port, ok, base+1)
	}
	if got := calls(t, argsLog); got == "" {
		t.Error("wake must start the container through StartService")
	}
	if _, running := m.RunningPort("svc:v1"); !running {
		t.Error("service must be running after wake")
	}
}

func TestWakeOnAccessFlagOff(t *testing.T) {
	bin, argsLog, _ := writeFakeCLI(t, "true 0")
	m := New(Config{
		Images: images.New(nil, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080}),
		Bin:    bin,
		ExtDir: t.TempDir(),
		Ports:  NewPortPool(2000, 2002),
	})
	if port, ok := m.WakeOnAccess("svc:v1"); ok || port != 0 {
		t.Errorf("flag off must keep the parked behaviour: %d %v", port, ok)
	}
	if calls(t, argsLog) != "" {
		t.Errorf("flag off must not start anything:\n%s", calls(t, argsLog))
	}
}

func TestRunServiceStartOnceSingleFlight(t *testing.T) {
	m := idleTestManager(t, images.Image{Name: "svc:v1", IsService: true, ServiceInternalPort: 8080})
	var starts int
	var mu sync.Mutex
	release := make(chan struct{})
	entered := make(chan struct{})

	worker := func() error {
		return m.runServiceStartOnce("svc:v1", func() error {
			mu.Lock()
			starts++
			mu.Unlock()
			close(entered)
			<-release
			return fmt.Errorf("boom")
		})
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = worker() }(i)
	}
	<-entered
	release <- struct{}{}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("concurrent wake ran start %d times, want 1", starts)
	}
	for i, err := range errs {
		if err == nil || err.Error() != "boom" {
			t.Errorf("worker %d got %v, want the shared error", i, err)
		}
	}
	// The guard clears after completion so a later request can retry.
	if err := m.runServiceStartOnce("svc:v1", func() error { return nil }); err != nil {
		t.Errorf("retry after completion failed: %v", err)
	}
}
