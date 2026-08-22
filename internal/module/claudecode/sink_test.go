package claudecode

import (
	"sort"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
)

// recordSink captures everything the adapter reports, for assertions.
type recordSink struct {
	mu        sync.Mutex
	emissions []domain.Activity // every Activity call, in order
	steps     []string
	usage     []domain.UsageDelta
	offsets   []int64
	outputs   []domain.RunOutput
}

func (s *recordSink) Activity(a domain.Activity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emissions = append(s.emissions, a)
}

func (s *recordSink) CurrentStep(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, step)
}

func (s *recordSink) Usage(u domain.UsageDelta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, u)
}

func (s *recordSink) Elicit(domain.Elicitation) error { return nil }

func (s *recordSink) Output(o domain.RunOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outputs = append(s.outputs, o)
}

func (s *recordSink) Offset(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets = append(s.offsets, n)
}

// final resolves the emission log the way the store would: re-emissions of a Seq replace the
// earlier version (the tool_result merge), and the result is ordered by Seq.
func (s *recordSink) final() []domain.Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	bySeq := map[int64]domain.Activity{}
	for _, a := range s.emissions {
		bySeq[a.Seq] = a
	}
	out := make([]domain.Activity, 0, len(bySeq))
	for _, a := range bySeq {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func (s *recordSink) usageTotal() domain.UsageDelta {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total domain.UsageDelta
	for _, u := range s.usage {
		total = total.Add(u)
	}
	return total
}

func (s *recordSink) lastOffset() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.offsets) == 0 {
		return 0
	}
	return s.offsets[len(s.offsets)-1]
}

func (s *recordSink) emissionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.emissions)
}

// stepClock is a deterministic clock: every Now call advances a fixed step, making timing
// fields and CreatedAt stable for the golden file.
type stepClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func newStepClock(start time.Time, step time.Duration) *stepClock {
	return &stepClock{t: start, step: step}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

// lockedBuffer is an io.WriteCloser recording everything written to it (the fake stdin).
type lockedBuffer struct {
	mu     sync.Mutex
	data   []byte
	closed bool
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *lockedBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
