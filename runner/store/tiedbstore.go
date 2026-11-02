package store

import (
	"fmt"
	"os"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/tiedb"
)

// jobFields is the full persisted-field vocabulary for a job. An upsert
// OpSets every one of them — present fields to their value, absent fields
// to empty — so the write is a REPLACE: a field that went empty (a cleared
// error message) cannot keep a stale value from the previous write.
var jobFields = []string{
	relUser, relHandler, relJobType, relInputHash, relOutputHash, relAppHash,
	relStatus, relErrorMsg, relLog, relPercent, relIsProd, relIsDebug,
	relDeployManager, relFilehost, relCreated, relStarted, relFinished,
	relReqCPU, relReqRAMMB, relMemPeakMB, relExitReason,
	relExtRef, relExtQueue, relExtRegion, relPipelineID, relStepName,
}

// jobFieldsNoLog is jobFields minus the container log. List and stats paths
// use it: logs can be megabytes per job, and no list consumer reads them.
var jobFieldsNoLog = func() []string {
	out := make([]string, 0, len(jobFields))
	for _, f := range jobFields {
		if f != relLog {
			out = append(out, f)
		}
	}
	return out
}()

var stepFields = []string{
	relStepName, relPipelineID, relOrdinal, relHandler, relJobType, relAppHash,
	relSettings, relInputs, relStatus, relJobID, relOutputHash, relErrorMsg,
	relStarted, relFinished,
}

var pipelineFields = []string{
	relName, relUser, relStatus, relErrorMsg, relDebug, relDeployManager,
	relCreated, relFinished, relStep,
}

// tieDB is the Store backed by the embedded tiedb engine: one directory,
// three collections, no external database process.
type tieDB struct {
	tree      *tiedb.TieTree
	jobs      *tiedb.Collection
	pipelines *tiedb.Collection
	steps     *tiedb.Collection
}

// NewStore opens (creating when needed) the store directory. The three
// collections are opened eagerly so a broken store fails at startup rather
// than on the first job write.
func NewStore(dir string) (Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store dir %s: %w", dir, err)
	}
	tree := tiedb.NewDB(true)
	tree.SetLocking(true) // an operator's direct `tie -f` cannot write under us
	t := &tieDB{tree: tree}
	var err error
	if t.jobs, err = tree.TryGetCollection(tiedb.CollectionKey{Database: dir, Collection: "jobs"}); err != nil {
		tree.Close()
		return nil, fmt.Errorf("store jobs collection: %w", err)
	}
	if t.pipelines, err = tree.TryGetCollection(tiedb.CollectionKey{Database: dir, Collection: "pipelines"}); err != nil {
		tree.Close()
		return nil, fmt.Errorf("store pipelines collection: %w", err)
	}
	if t.steps, err = tree.TryGetCollection(tiedb.CollectionKey{Database: dir, Collection: "pipeline-steps"}); err != nil {
		tree.Close()
		return nil, fmt.Errorf("store pipeline-steps collection: %w", err)
	}
	return t, nil
}

func (t *tieDB) Close() error {
	t.tree.Close()
	return nil
}

// opsFrom builds a full-replace Apply batch for one subject: every field in
// the vocabulary is Set — to its encoded value, or to nothing when the
// encoded map omits it.
func opsFrom(key string, fields []string, attrs map[string]string) []tiedb.Op {
	ops := make([]tiedb.Op, 0, len(fields))
	for _, f := range fields {
		values := []string(nil)
		if v, ok := attrs[f]; ok {
			values = []string{v}
		}
		ops = append(ops, tiedb.Op{Kind: tiedb.OpSet, Key: key, Relation: f, Values: values})
	}
	return ops
}

func (t *tieDB) UpsertJob(job *api.JobStatus) {
	if _, err := t.jobs.Apply(opsFrom(job.JobID, jobFields, encodeJob(job))); err != nil {
		logStoreError("persist job", "job_id", job.JobID, "error", err)
	}
}

// fetchFields reads one relation per ExpandKeys pass: the engine's relation
// filter keeps big fields (the container log) out of list and stats reads
// entirely rather than loading them to drop them.
func fetchFields(col *tiedb.Collection, keys []string, fields []string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(keys))
	for _, rel := range fields {
		for _, row := range col.ExpandKeys(keys, rel) {
			if vs := row.Attributes[rel]; len(vs) > 0 {
				if out[row.Key] == nil {
					out[row.Key] = map[string]string{}
				}
				out[row.Key][rel] = vs[0]
			}
		}
	}
	return out
}

func (t *tieDB) GetJob(jobID string) (*api.JobStatus, bool) {
	attrs := fetchFields(t.jobs, []string{jobID}, jobFields)
	a, ok := attrs[jobID]
	if !ok {
		return nil, false
	}
	return decodeJob(jobID, a), true
}

// scanWith lists every subject that carries the given relation, as
// (subject, relation, value) triples. It is the enumeration the list and
// reconcile paths start from; the collection stays small because the
// retention reaper bounds history.
func scanWith(col *tiedb.Collection, relation string) []tiedb.StringTriple {
	sorted, _, _ := col.QueryTags(tiedb.TagQuery{
		HasRelation: relation,
		Sort:        tiedb.SortOptions{Limit: -1},
	})
	return sorted
}

func (t *tieDB) ListJobs(sinceUnix int64, user, status string) ([]*api.JobStatus, error) {
	keys := createdKeysAfter(t.jobs, sinceUnix)
	if len(keys) == 0 {
		return nil, nil
	}
	attrs := fetchFields(t.jobs, keys, jobFieldsNoLog)
	var out []*api.JobStatus
	for _, key := range keys {
		a, ok := attrs[key]
		if !ok {
			continue
		}
		if user != "" && a[relUser] != user {
			continue
		}
		if status != "" && a[relStatus] != status {
			continue
		}
		a[relLog] = "" // list responses never carry logs
		out = append(out, decodeJob(key, a))
	}
	sortJobsNewestFirst(out)
	return out, nil
}

// createdKeysAfter returns the subjects whose padded "created" value is at
// or after since. Padded widths make the string compare numeric.
func createdKeysAfter(col *tiedb.Collection, sinceUnix int64) []string {
	triples := scanWith(col, relCreated)
	min := padInt(sinceUnix)
	var keys []string
	for _, tr := range triples {
		if tr.Value2 >= min {
			keys = append(keys, tr.Key)
		}
	}
	return keys
}

func (t *tieDB) Stats(sinceUnix int64) (*api.JobStats, error) {
	keys := createdKeysAfter(t.jobs, sinceUnix)
	statFields := jobFieldsNoLog
	attrs := fetchFields(t.jobs, keys, statFields)
	rows := make([]map[string]string, 0, len(attrs))
	for _, a := range attrs {
		rows = append(rows, a)
	}
	return aggregateStats(rows), nil
}

func (t *tieDB) ReconcileInterrupted() (int, error) {
	triples := scanWith(t.jobs, relStatus)
	var ops []tiedb.Op
	count := 0
	for _, tr := range triples {
		if tr.Value2 != api.StatusQueued && tr.Value2 != api.StatusRunning {
			continue
		}
		// Externally executed jobs keep running across a restart; the
		// poller resumes tracking them, so leave them non-terminal.
		if vs, _ := t.jobs.Get(tr.Key, relExtRef); len(vs) > 0 && vs[0] != "" {
			continue
		}
		ops = append(ops,
			tiedb.Op{Kind: tiedb.OpSet, Key: tr.Key, Relation: relStatus, Values: []string{api.StatusFailed}},
			tiedb.Op{Kind: tiedb.OpSet, Key: tr.Key, Relation: relErrorMsg, Values: []string{"Interrupted by runner restart"}},
		)
		count++
	}
	if len(ops) == 0 {
		return 0, nil
	}
	if _, err := t.jobs.Apply(ops); err != nil {
		return 0, err
	}
	return count, nil
}

func (t *tieDB) ListNonTerminalExternalJobs() ([]*api.JobStatus, error) {
	triples := scanWith(t.jobs, relStatus)
	var keys []string
	for _, tr := range triples {
		if tr.Value2 != api.StatusQueued && tr.Value2 != api.StatusRunning {
			continue
		}
		if vs, _ := t.jobs.Get(tr.Key, relExtRef); len(vs) > 0 && vs[0] != "" {
			keys = append(keys, tr.Key)
		}
	}
	attrs := fetchFields(t.jobs, keys, jobFields)
	var out []*api.JobStatus
	for _, key := range keys {
		if a, ok := attrs[key]; ok {
			out = append(out, decodeJob(key, a))
		}
	}
	sortJobsNewestFirst(out)
	return out, nil
}

func (t *tieDB) InsertPipeline(p *api.Pipeline) error {
	pa, steps := encodePipeline(p)
	// Steps first, pipeline row last: the pipeline's "step" relation is
	// what makes steps reachable, so a crash between the two batches
	// leaves unreachable step subjects (harmless garbage) rather than a
	// pipeline naming steps that do not exist (which advance would treat
	// as satisfied-and-missing).
	for key, sa := range steps {
		attrs := map[string]string{relPipelineID: p.PipelineID}
		for f, v := range sa {
			attrs[f] = v
		}
		if _, err := t.steps.Apply(opsFrom(key, stepFields, attrs)); err != nil {
			return fmt.Errorf("insert pipeline %s steps: %w", p.PipelineID, err)
		}
	}
	if _, err := t.pipelines.Apply(opsFrom(p.PipelineID, pipelineFields, pa)); err != nil {
		return fmt.Errorf("insert pipeline %s: %w", p.PipelineID, err)
	}
	return nil
}

func (t *tieDB) UpsertPipelineStep(pipelineID string, step *api.PipelineStep) {
	sa := encodeStep(step)
	sa[relPipelineID] = pipelineID
	if _, err := t.steps.Apply(opsFrom(stepKey(pipelineID, step.Name), stepFields, sa)); err != nil {
		logStoreError("persist pipeline step", "pipeline_id", pipelineID, "step", step.Name, "error", err)
	}
}

func (t *tieDB) UpdatePipelineStatus(pipelineID, status, errorMsg string, finishedUnix int64) {
	ops := []tiedb.Op{
		{Kind: tiedb.OpSet, Key: pipelineID, Relation: relStatus, Values: nonEmpty(status)},
		{Kind: tiedb.OpSet, Key: pipelineID, Relation: relErrorMsg, Values: nonEmpty(errorMsg)},
		{Kind: tiedb.OpSet, Key: pipelineID, Relation: relFinished, Values: nonEmpty(padInt(finishedUnix))},
	}
	if _, err := t.pipelines.Apply(ops); err != nil {
		logStoreError("persist pipeline status", "pipeline_id", pipelineID, "error", err)
	}
}

func (t *tieDB) GetPipeline(pipelineID string) (*api.Pipeline, bool) {
	rows := t.pipelines.ExpandKeys([]string{pipelineID}, "")
	if len(rows) == 0 {
		return nil, false
	}
	a := map[string]string{}
	for rel, vs := range rows[0].Attributes {
		if len(vs) > 0 {
			a[rel] = vs[0]
		}
	}
	p := decodePipeline(a, t.loadSteps(pipelineID, stepNames(a)))
	p.PipelineID = pipelineID
	return p, true
}

func (t *tieDB) loadSteps(pipelineID string, names []string) []map[string]string {
	keys := make([]string, 0, len(names))
	for _, n := range names {
		keys = append(keys, stepKey(pipelineID, n))
	}
	byKey := fetchFields(t.steps, keys, stepFields)
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		if a, ok := byKey[k]; ok {
			out = append(out, a)
		}
	}
	return out
}

func (t *tieDB) ListPipelines(sinceUnix int64, user, status string) ([]*api.Pipeline, error) {
	keys := createdKeysAfter(t.pipelines, sinceUnix)
	attrs := fetchFields(t.pipelines, keys, pipelineFields)
	var out []*api.Pipeline
	for _, key := range keys {
		a, ok := attrs[key]
		if !ok {
			continue
		}
		if user != "" && a[relUser] != user {
			continue
		}
		if status != "" && a[relStatus] != status {
			continue
		}
		p := decodePipeline(a, t.loadSteps(key, stepNames(a)))
		p.PipelineID = key
		out = append(out, p)
	}
	sortPipelinesNewestFirst(out)
	return out, nil
}

func (t *tieDB) ListRunningPipelines() ([]*api.Pipeline, error) {
	return t.ListPipelines(0, "", api.StatusRunning)
}

func (t *tieDB) ReapRetention(keepDays int) (int, int, error) {
	if keepDays <= 0 {
		return 0, 0, nil
	}
	cutoff := time.Now().Unix() - int64(keepDays)*86400
	jobs := 0
	for _, key := range createdKeysBefore(t.jobs, cutoff) {
		if err := deleteSubject(t.jobs, key); err != nil {
			return jobs, 0, err
		}
		jobs++
	}
	pipes := 0
	for _, key := range createdKeysBefore(t.pipelines, cutoff) {
		rows := t.pipelines.ExpandKeys([]string{key}, relStep)
		if len(rows) > 0 {
			var names []string
			if vs := rows[0].Attributes[relStep]; len(vs) > 0 {
				names = stepNames(map[string]string{relStep: vs[0]})
			}
			for _, n := range names {
				if err := deleteSubject(t.steps, stepKey(key, n)); err != nil {
					return jobs, pipes, err
				}
			}
		}
		if err := deleteSubject(t.pipelines, key); err != nil {
			return jobs, pipes, err
		}
		pipes++
	}
	return jobs, pipes, nil
}

func createdKeysBefore(col *tiedb.Collection, beforeUnix int64) []string {
	triples := scanWith(col, relCreated)
	max := padInt(beforeUnix)
	var keys []string
	for _, tr := range triples {
		if tr.Value2 < max {
			keys = append(keys, tr.Key)
		}
	}
	return keys
}

// deleteSubject removes every triple under a subject in one batch.
func deleteSubject(col *tiedb.Collection, key string) error {
	rows := col.ExpandKeys([]string{key}, "")
	if len(rows) == 0 {
		return nil
	}
	var ops []tiedb.Op
	for rel, vs := range rows[0].Attributes {
		for _, v := range vs {
			ops = append(ops, tiedb.Op{Kind: tiedb.OpDelete, Key: key, Relation: rel, Values: []string{v}})
		}
	}
	_, err := col.Apply(ops)
	return err
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
