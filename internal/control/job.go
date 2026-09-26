package control

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// One abstraction for an operation that outlives the request that started it.
//
// There were hand-rolled versions of this before. The link test kept a
// mutex, a bool and a pointer in a package-level struct literal; the staged
// fleet rollout was about to want another, and that
// one fans out across servers. None of them had progress, cancellation or any
// memory of what happened last time — so the panel showed a spinner and, when
// it finished, showed the answer or nothing.
//
// Ad-hoc versions of the same thing is how they drift. This is the one.
//
// # One at a time, per kind
//
// A job has a kind, and only one job of a kind runs at once. That is not a
// limitation being worked around: two link tests at once measure each other
// rather than the path. Starting a second is
// refused with the first one's handle, so a caller that raced sees the running
// job rather than an error.

// State is where a job is.
type State string

const (
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Done reports whether the job has finished, however it finished.
func (s State) Done() bool { return s != Running }

// Job is one operation, as a caller reads it.
//
// It is a value: Get and History hand back copies, so a caller can read a job
// without holding a lock and without seeing it change underneath them.
type Job struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"` // what it is operating on
	State  State  `json:"state"`

	// Step is what it is doing now, in the operator's words. It is a string
	// and not a percentage because most of these operations do not know their
	// own length — a fleet rollout knows which server it is on, and a speed
	// test knows nothing at all until it ends.
	Step string `json:"step,omitempty"`

	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`

	// Result is whatever the operation produced. Typed as any because the
	// alternative is a jobs package that knows about link tests.
	Result any `json:"result,omitempty"`

	// Err is why it failed, in the operator's words.
	Err string `json:"err,omitempty"`
}

// Elapsed is how long the job ran, or has been running.
func (j Job) Elapsed() time.Duration {
	if j.Ended.IsZero() {
		return time.Since(j.Started)
	}
	return j.Ended.Sub(j.Started)
}

// Progress is what a running job reports through. It is handed to the work
// function so that reporting does not mean reaching back for the registry.
type Progress struct {
	jobs *Jobs
	id   string
}

// Step records what the job is doing now. Safe to call from any goroutine, and
// safe to call on a job that has already been cancelled.
func (p *Progress) Step(format string, a ...any) {
	if p == nil || p.jobs == nil {
		return
	}
	p.jobs.step(p.id, fmt.Sprintf(format, a...))
}

// Jobs is the registry. The zero value is not usable; call NewJobs.
type Jobs struct {
	mu      sync.Mutex
	live    map[string]*entry // by id
	byKind  map[string]string // kind -> the id currently running
	history []Job             // finished, oldest first

	// keep is how many finished jobs are remembered. Enough that an operator
	// who looked away during a rollout can still read what happened, small
	// enough to stay in memory without a policy.
	keep int

	// seq numbers ids. A counter rather than a random string because these are
	// read out of a URL by a person as often as by a browser.
	seq int
	// now is time.Now, replaced in tests.
	now func() time.Time
}

type entry struct {
	job    Job
	cancel context.CancelFunc
}

// NewJobs builds a registry.
func NewJobs() *Jobs {
	return &Jobs{
		live:   map[string]*entry{},
		byKind: map[string]string{},
		keep:   50,
		now:    time.Now,
	}
}

// ErrBusy is returned when a job of this kind is already running. It carries
// that job, because a caller that raced wants the running one rather than an
// apology.
type ErrBusy struct{ Running Job }

func (e ErrBusy) Error() string {
	return fmt.Sprintf("a %s is already running (%s)", e.Running.Kind, e.Running.ID)
}

// Start runs work in its own goroutine and returns immediately.
//
// work is given a context that is cancelled by Cancel or by the parent, and a
// Progress to report through. Whatever it returns becomes the job's Result; a
// non-nil error makes it Failed.
//
// The parent context matters: a panel that has shut down must not leave a fleet
// rollout running against servers nobody is watching.
func (j *Jobs) Start(parent context.Context, kind, target string,
	work func(ctx context.Context, p *Progress) (any, error)) (Job, error) {

	j.mu.Lock()
	if id, ok := j.byKind[kind]; ok {
		running := j.live[id].job
		j.mu.Unlock()
		return running, ErrBusy{Running: running}
	}

	j.seq++
	id := fmt.Sprintf("%s-%d", kind, j.seq)
	ctx, cancel := context.WithCancel(parent)
	job := Job{ID: id, Kind: kind, Target: target, State: Running, Started: j.now()}
	j.live[id] = &entry{job: job, cancel: cancel}
	j.byKind[kind] = id
	j.mu.Unlock()

	go func() {
		res, err := work(ctx, &Progress{jobs: j, id: id})
		j.finish(id, res, err, ctx.Err() != nil)
	}()
	return job, nil
}

// finish moves a job out of the live set and into history.
func (j *Jobs) finish(id string, res any, err error, cancelled bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	e, ok := j.live[id]
	if !ok {
		return
	}
	e.job.Ended = j.now()
	e.job.Result = res
	switch {
	case cancelled:
		e.job.State = Cancelled
		// A cancelled job's error is almost always "context canceled", which
		// tells an operator nothing they did not already know.
		e.job.Err = ""
	case err != nil:
		e.job.State = Failed
		e.job.Err = err.Error()
	default:
		e.job.State = Succeeded
	}
	e.cancel()

	j.history = append(j.history, e.job)
	if len(j.history) > j.keep {
		j.history = j.history[len(j.history)-j.keep:]
	}
	delete(j.live, id)
	if j.byKind[e.job.Kind] == id {
		delete(j.byKind, e.job.Kind)
	}
}

func (j *Jobs) step(id, text string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e, ok := j.live[id]; ok {
		e.job.Step = text
	}
}

// Get returns one job, running or finished.
func (j *Jobs) Get(id string) (Job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e, ok := j.live[id]; ok {
		return e.job, true
	}
	for i := len(j.history) - 1; i >= 0; i-- {
		if j.history[i].ID == id {
			return j.history[i], true
		}
	}
	return Job{}, false
}

// Current returns the running job of this kind, if there is one.
func (j *Jobs) Current(kind string) (Job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	id, ok := j.byKind[kind]
	if !ok {
		return Job{}, false
	}
	return j.live[id].job, true
}

// Latest returns the running job of this kind, or the most recently finished
// one — which is what a panel polling after a refresh actually wants: "what is
// happening, or what just happened".
func (j *Jobs) Latest(kind string) (Job, bool) {
	if job, ok := j.Current(kind); ok {
		return job, true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := len(j.history) - 1; i >= 0; i-- {
		if j.history[i].Kind == kind {
			return j.history[i], true
		}
	}
	return Job{}, false
}

// History returns finished jobs of this kind, newest first. An empty kind
// returns every kind.
func (j *Jobs) History(kind string, limit int) []Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Job
	for i := len(j.history) - 1; i >= 0; i-- {
		if kind != "" && j.history[i].Kind != kind {
			continue
		}
		out = append(out, j.history[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Cancel stops a running job. Cancelling one that has already finished is not
// an error: the caller is racing the job, and it losing that race is the
// ordinary case rather than a mistake.
func (j *Jobs) Cancel(id string) bool {
	j.mu.Lock()
	e, ok := j.live[id]
	j.mu.Unlock()
	if !ok {
		return false
	}
	e.cancel()
	return true
}

// ResultOf reads a finished job's result as the type that kind produces.
//
// # Why Result is `any` and stays that way
//
// The obvious fix is to make Jobs generic over the result type. It does not
// work: one registry holds every kind at once — a link test producing a
// measurement, a fleet rollout producing a report — and a registry generic over
// one T can hold only one of them. A registry per kind is more machinery for
// less, and it loses the thing the registry is for, which is that "what is
// running" has a single answer.
//
// So the type lives at the call site, and what this adds is that getting it
// wrong says so. A bare assertion that fails produces the zero value and a
// false, which at a call site that ignores the second return is a job that
// silently reports nothing — the panel shows a spinner that never resolves and
// no line anywhere says why.
//
// ok is false for a job that has not finished, one that failed, and one whose
// result is not a T. The three are different and the caller usually wants to
// distinguish them, which is why Job is returned rather than swallowed.
func ResultOf[T any](j Job) (T, bool) {
	var zero T
	if j.State != Succeeded || j.Result == nil {
		return zero, false
	}
	v, ok := j.Result.(T)
	if !ok {
		return zero, false
	}
	return v, true
}
