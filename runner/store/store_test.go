package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/uidbz/tie/io/compose"
	"github.com/uidbz/tie/runner/api"
)

// Every behavior is asserted against both implementations through the same
// body: the tiedb store (the real one) and the fake (what downstream tests
// will use). If they disagree, this file fails.

func newStores(t *testing.T) map[string]Store {
	t.Helper()
	db, err := NewStore(t.TempDir() + "/runner-store")
	if err != nil {
		t.Fatalf("opening tiedb store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return map[string]Store{
		"tiedb": db,
		"fake":  NewFake(),
	}
}

func eachStore(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

const day = 86400

func sampleJob(id string, created int64) *api.JobStatus {
	return &api.JobStatus{
		JobID: id, User: "alice", Handler: "mzmine:latest", JobType: "CPUv1",
		InputHash: "in1", OutputHash: "out1", AppHash: "app1",
		Status: api.StatusDone, ErrorMsg: "", Log: "container log for " + id,
		Percent: 100, DeployManager: "https://runner.example", Filehost: "https://fh.example",
		ExitReason: "normal", ReqCPU: 4, ReqRAMMB: 8192, MemPeakMB: 4321,
		PipelineID: "p1", StepName: "step-a",
		CreatedUnix: created, StartedUnix: created + 10, FinishedUnix: created + 120,
		IsDebug: true, IsProd: false,
	}
}

func TestJobRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now().Unix()
		j := sampleJob("j1", now)
		s.UpsertJob(j)

		got, ok := s.GetJob("j1")
		if !ok {
			t.Fatal("job not found after upsert")
		}
		want := *j
		want.MemCurrentMB = 0 // live-only: not persisted, by design
		applyStatusFlags(&want)
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("round trip differs:\n got %+v\nwant %+v", *got, want)
		}

		if _, ok := s.GetJob("nope"); ok {
			t.Error("absent job reported present")
		}

		// Upsert replaces: a field that goes empty must not keep its old value.
		j.Status = api.StatusRunning
		j.ErrorMsg = "retrying"
		s.UpsertJob(j)
		j.ErrorMsg = ""
		j.OutputHash = ""
		s.UpsertJob(j)
		got, _ = s.GetJob("j1")
		if got.ErrorMsg != "" || got.OutputHash != "" {
			t.Errorf("replace semantics: ErrorMsg=%q OutputHash=%q, want empty", got.ErrorMsg, got.OutputHash)
		}
		if !got.IsRunning || got.Done {
			t.Errorf("derived flags wrong for running: IsRunning=%v Done=%v", got.IsRunning, got.Done)
		}
		if got.RunningTime == "" {
			t.Error("RunningTime not derived")
		}
	})
}

func TestListJobs(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now().Unix()
		jobs := []*api.JobStatus{
			sampleJob("old", now-10*day),
			sampleJob("a-new", now),
			sampleJob("b-new", now),
		}
		jobs[0].User = "bob"
		jobs[0].Status = api.StatusFailed
		jobs[1].Status = api.StatusRunning
		for _, j := range jobs {
			s.UpsertJob(j)
		}

		all, err := s.ListJobs(now-30*day, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 3 {
			t.Fatalf("want 3 jobs, got %d", len(all))
		}
		if all[0].JobID != "b-new" || all[1].JobID != "a-new" || all[2].JobID != "old" {
			t.Errorf("not newest first (id tie-break): %s %s %s", all[0].JobID, all[1].JobID, all[2].JobID)
		}
		for _, j := range all {
			if j.Log != "" {
				t.Errorf("ListJobs must not carry logs (%s has %d bytes)", j.JobID, len(j.Log))
			}
		}

		recent, _ := s.ListJobs(now-2*day, "", "")
		if len(recent) != 2 {
			t.Errorf("since filter: want 2, got %d", len(recent))
		}
		bob, _ := s.ListJobs(0, "bob", "")
		if len(bob) != 1 || bob[0].JobID != "old" {
			t.Errorf("user filter: %+v", bob)
		}
		running, _ := s.ListJobs(0, "", api.StatusRunning)
		if len(running) != 1 || running[0].JobID != "a-new" {
			t.Errorf("status filter: %+v", running)
		}
	})
}

func TestStatsAggregates(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix()
		j := sampleJob("s1", base) // done, 110s, 4321 MB, debug
		s.UpsertJob(j)
		j = sampleJob("s2", base+day)
		j.Handler = "mastertable:latest"
		j.Status = api.StatusFailed
		j.ExitReason = "out-of-memory"
		j.User = "bob"
		j.StartedUnix, j.FinishedUnix = base+day+5, base+day+65 // 60s
		j.MemPeakMB = 1000
		j.IsDebug, j.IsProd = false, true
		s.UpsertJob(j)
		j = sampleJob("s3", base+2*day)
		j.Status = api.StatusCancelled
		j.ErrorMsg = "user cancelled"
		j.StartedUnix, j.FinishedUnix = 0, 0 // never ran: excluded from runtime stats
		j.MemPeakMB = 0
		s.UpsertJob(j)

		st, err := s.Stats(base - day)
		if err != nil {
			t.Fatal(err)
		}
		if st.Total != 3 || st.Succeeded != 1 || st.Failed != 1 || st.Cancelled != 1 {
			t.Errorf("counts: %+v", st)
		}
		if st.SuccessRate != 1.0/3.0 {
			t.Errorf("SuccessRate = %v, want 1/3", st.SuccessRate)
		}
		if st.AvgRuntimeSeconds != 85 || st.MaxRuntimeSeconds != 110 {
			t.Errorf("runtime: avg %v max %v, want 85/110", st.AvgRuntimeSeconds, st.MaxRuntimeSeconds)
		}
		if st.AvgPeakMemMB != 2660.5 || st.MaxPeakMemMB != 4321 {
			t.Errorf("mem: avg %v max %v, want 2660.5/4321", st.AvgPeakMemMB, st.MaxPeakMemMB)
		}
		if st.OOMKills != 1 || st.DebugJobs != 2 || st.ProdJobs != 1 {
			t.Errorf("flags: oom=%d debug=%d prod=%d", st.OOMKills, st.DebugJobs, st.ProdJobs)
		}
		wantMonths := []api.MonthCount{{Month: "2026-10", Count: 3}}
		if !reflect.DeepEqual(st.MonthlyUsage, wantMonths) {
			t.Errorf("MonthlyUsage = %v, want %v", st.MonthlyUsage, wantMonths)
		}
		if len(st.JobsByUser) != 2 || st.JobsByUser[0].Name != "alice" || st.JobsByUser[0].Count != 2 {
			t.Errorf("JobsByUser = %v, want alice(2) first", st.JobsByUser)
		}
		if st.ActiveUsers["2026-10"] == nil || len(st.ActiveUsers["2026-10"]) != 2 {
			t.Errorf("ActiveUsers = %v", st.ActiveUsers)
		}

		// The since window excludes the first job.
		st, _ = s.Stats(base + day)
		if st.Total != 2 {
			t.Errorf("since window: want 2, got %d", st.Total)
		}
	})
}

func TestReconcileAndExternalResume(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now().Unix()
		mk := func(id, status, extRef string) {
			j := sampleJob(id, now)
			j.Status = status
			j.BatchJobID, j.Queue, j.Region = extRef, "q", "r"
			j.Log = "log-" + id
			s.UpsertJob(j)
		}
		mk("queued-1", api.StatusQueued, "")
		mk("running-1", api.StatusRunning, "")
		mk("ext-running", api.StatusRunning, "batch-42")
		mk("done-1", api.StatusDone, "")

		n, err := s.ReconcileInterrupted()
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("reconciled %d, want 2", n)
		}
		for _, id := range []string{"queued-1", "running-1"} {
			j, _ := s.GetJob(id)
			if j.Status != api.StatusFailed || j.ErrorMsg != "Interrupted by runner restart" {
				t.Errorf("%s: status=%q err=%q", id, j.Status, j.ErrorMsg)
			}
		}
		j, _ := s.GetJob("ext-running")
		if j.Status != api.StatusRunning {
			t.Errorf("externally executed job was reconciled: %q", j.Status)
		}

		resumable, err := s.ListNonTerminalExternalJobs()
		if err != nil {
			t.Fatal(err)
		}
		if len(resumable) != 1 || resumable[0].JobID != "ext-running" || resumable[0].Log != "log-ext-running" {
			t.Errorf("resumable set: %+v", resumable)
		}

		// Idempotent: a second pass has nothing left to do.
		n, _ = s.ReconcileInterrupted()
		if n != 0 {
			t.Errorf("second reconcile pass: %d, want 0", n)
		}
	})
}

func samplePipeline(id string, created int64) *api.Pipeline {
	return &api.Pipeline{
		PipelineID: id, Name: "discovery", User: "alice", Status: api.StatusRunning,
		DeployManager: "https://runner.example", Debug: true, CreatedUnix: created,
		Steps: []*api.PipelineStep{
			{Name: "raw", Ordinal: -1, Status: api.StepDone, OutputHash: "seed1"},
			{
				Name: "mzmine", Ordinal: 0, Handler: "mzmine:latest", JobType: "CPUv1",
				AppHash:  "app1",
				Settings: json.RawMessage(`{"tolerance":0.005}`),
				Status:   api.StepPending,
				Inputs:   []compose.Spec{{Hash: "seed1"}},
			},
			{
				Name: "mastertable", Ordinal: 1, Handler: "mastertable:latest", JobType: "CPUv1",
				Status: api.StepPending,
				Inputs: []compose.Spec{
					{FromStep: "mzmine", Paths: []string{"results/all_data.csv"}, As: "all_data.csv"},
					{FromStep: "mzmine", Glob: "retrieval/*_mist_cosine.tsv", Optional: true},
				},
			},
		},
	}
}

func pipelineEqual(got, want *api.Pipeline) error {
	if got.Name != want.Name || got.User != want.User || got.Status != want.Status ||
		got.ErrorMsg != want.ErrorMsg || got.DeployManager != want.DeployManager ||
		got.Debug != want.Debug || got.CreatedUnix != want.CreatedUnix || got.FinishedUnix != want.FinishedUnix {
		return fmt.Errorf("pipeline fields differ:\n got %+v\nwant %+v", got, want)
	}
	if len(got.Steps) != len(want.Steps) {
		return fmt.Errorf("step count %d, want %d", len(got.Steps), len(want.Steps))
	}
	for i := range got.Steps {
		g, w := got.Steps[i], want.Steps[i]
		if g.Name != w.Name || g.Ordinal != w.Ordinal || g.Handler != w.Handler || g.JobType != w.JobType ||
			g.AppHash != w.AppHash || g.Status != w.Status || g.JobID != w.JobID ||
			g.OutputHash != w.OutputHash || g.ErrorMsg != w.ErrorMsg ||
			g.StartedUnix != w.StartedUnix || g.FinishedUnix != w.FinishedUnix ||
			string(g.Settings) != string(w.Settings) {
			return fmt.Errorf("step %d differs:\n got %+v\nwant %+v", i, g, w)
		}
		if !reflect.DeepEqual(g.Inputs, w.Inputs) {
			return fmt.Errorf("step %s inputs differ:\n got %+v\nwant %+v", w.Name, g.Inputs, w.Inputs)
		}
	}
	return nil
}

func TestPipelineRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now().Unix()
		p := samplePipeline("p1", now)
		if err := s.InsertPipeline(p); err != nil {
			t.Fatal(err)
		}
		got, ok := s.GetPipeline("p1")
		if !ok {
			t.Fatal("pipeline not found")
		}
		if got.PipelineID != "p1" {
			t.Errorf("PipelineID = %q", got.PipelineID)
		}
		// Steps come back sorted by ordinal: seed first.
		if err := pipelineEqual(got, p); err != nil {
			t.Error(err)
		}

		// Step progress updates in place; the step graph does not grow.
		step := p.Steps[1]
		step.Status, step.JobID = api.StepRunning, "job-9"
		step.StartedUnix = now + 5
		s.UpsertPipelineStep("p1", step)
		step.Status, step.OutputHash, step.FinishedUnix = api.StepDone, "out9", now+300
		s.UpsertPipelineStep("p1", step)

		s.UpdatePipelineStatus("p1", api.StatusDone, "", now+400)

		got, _ = s.GetPipeline("p1")
		if got.Status != api.StatusDone || got.FinishedUnix != now+400 {
			t.Errorf("pipeline status: %q finished=%d", got.Status, got.FinishedUnix)
		}
		if len(got.Steps) != 3 {
			t.Fatalf("step count after in-place upsert: %d, want 3", len(got.Steps))
		}
		if st := got.Steps[1]; st.Status != api.StepDone || st.JobID != "job-9" || st.OutputHash != "out9" ||
			st.StartedUnix != now+5 || st.FinishedUnix != now+300 {
			t.Errorf("step progress did not survive: %+v", st)
		}

		// List loads steps for every pipeline.
		all, err := s.ListPipelines(0, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 1 || len(all[0].Steps) != 3 || all[0].Steps[0].Name != "raw" {
			t.Errorf("ListPipelines: %+v", all)
		}

		if _, ok := s.GetPipeline("absent"); ok {
			t.Error("absent pipeline reported present")
		}
	})
}

func TestListRunningPipelines(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now().Unix()
		if err := s.InsertPipeline(samplePipeline("running", now)); err != nil {
			t.Fatal(err)
		}
		p := samplePipeline("failed", now-day)
		p.Status = api.StatusFailed
		if err := s.InsertPipeline(p); err != nil {
			t.Fatal(err)
		}
		running, err := s.ListRunningPipelines()
		if err != nil {
			t.Fatal(err)
		}
		if len(running) != 1 || running[0].PipelineID != "running" {
			t.Errorf("running pipelines: %+v", running)
		}
	})
}

func TestRetentionReap(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		now := time.Now().Unix()
		old := sampleJob("ancient", now-40*day)
		s.UpsertJob(old)
		s.UpsertJob(sampleJob("fresh", now))
		if err := s.InsertPipeline(samplePipeline("ancient-p", now-40*day)); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertPipeline(samplePipeline("fresh-p", now)); err != nil {
			t.Fatal(err)
		}

		jobs, pipes, err := s.ReapRetention(30)
		if err != nil {
			t.Fatal(err)
		}
		if jobs != 1 || pipes != 1 {
			t.Errorf("reaped jobs=%d pipelines=%d, want 1/1", jobs, pipes)
		}
		if _, ok := s.GetJob("ancient"); ok {
			t.Error("old job survived the reaper")
		}
		if _, ok := s.GetPipeline("ancient-p"); ok {
			t.Error("old pipeline survived the reaper")
		}
		if _, ok := s.GetJob("fresh"); !ok {
			t.Error("recent job was reaped")
		}
		if p, ok := s.GetPipeline("fresh-p"); !ok || len(p.Steps) != 3 {
			t.Errorf("recent pipeline lost its steps: ok=%v", ok)
		}
		// 0 disables retention.
		jobs, pipes, _ = s.ReapRetention(0)
		if jobs != 0 || pipes != 0 {
			t.Error("keepDays 0 must be a no-op")
		}
	})
}

// TestDurabilityAcrossReopen is the whole point of the durable store: close
// and reopen the directory and every read path must see the same state.
func TestDurabilityAcrossReopen(t *testing.T) {
	dir := t.TempDir() + "/runner-store"
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	j := sampleJob("d1", now)
	j.Log = "multi\nline\nlog"
	s.UpsertJob(j)
	if err := s.InsertPipeline(samplePipeline("dp", now)); err != nil {
		t.Fatal(err)
	}
	step := &api.PipelineStep{Name: "mzmine", Ordinal: 0, Status: api.StepRunning, JobID: "job-7"}
	s.UpsertPipelineStep("dp", step)
	s.Close()

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, ok := s2.GetJob("d1")
	if !ok {
		t.Fatal("job lost across reopen")
	}
	if got.Log != "multi\nline\nlog" || got.User != "alice" || !got.Done {
		t.Errorf("job degraded across reopen: %+v", got)
	}
	p, ok := s2.GetPipeline("dp")
	if !ok {
		t.Fatal("pipeline lost across reopen")
	}
	if len(p.Steps) != 3 || p.Steps[1].Status != api.StepRunning || p.Steps[1].JobID != "job-7" {
		t.Errorf("pipeline steps degraded across reopen: %+v", p.Steps)
	}
	if running, _ := s2.ListRunningPipelines(); len(running) != 1 {
		t.Errorf("running pipelines after reopen: %d", len(running))
	}
}

// Interface conformance, compile-time. The tiedb store's conformance is
// structural: NewStore's declared return type is Store.
var _ Store = (*Fake)(nil)
