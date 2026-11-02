package scheduler

import "sync"

// Queue is the FIFO of local jobs waiting for admission. The admission
// loop scans it (List) rather than popping, because scan-and-fit may
// skip jobs that don't currently start; Remove pulls a job out when it
// is admitted or cancelled.
type Queue struct {
	items []*Job
	mu    sync.Mutex
}

// NewQueue returns an empty queue.
func NewQueue() *Queue { return &Queue{} }

// Push appends a job.
func (q *Queue) Push(job *Job) {
	q.mu.Lock()
	q.items = append(q.items, job)
	q.mu.Unlock()
}

// Remove drops the job with this id, reporting whether it was queued.
func (q *Queue) Remove(jobID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.items {
		if j.JobID == jobID {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true
		}
	}
	return false
}

// List returns a snapshot copy of the queued jobs in FIFO order.
func (q *Queue) List() []*Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*Job, len(q.items))
	copy(out, q.items)
	return out
}

// Size returns the queue length.
func (q *Queue) Size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
