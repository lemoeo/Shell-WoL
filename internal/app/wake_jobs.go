package app

import (
	"context"
	"errors"
	"sync"
	"time"
)

type WakeJob struct {
	ID       string     `json:"id"`
	DeviceID string     `json:"device_id"`
	State    string     `json:"state"`
	Progress string     `json:"progress"`
	Result   WakeResult `json:"result"`
	Updated  time.Time  `json:"updated"`
}
type wakeJobs struct {
	mu     sync.Mutex
	jobs   map[string]WakeJob
	active map[string]string
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
	run    func(context.Context, Device, []byte, func(string)) WakeResult
}

var errJobsBusy = errors.New("Mac 唤醒任务繁忙，请稍后重试")
var errDeviceBusy = errors.New("设备唤醒任务执行中，请完成后再编辑或删除")

func (j *wakeJobs) withIdleDevice(id string, update func() error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.active[id] != "" {
		return errDeviceBusy
	}
	return update()
}

func newWakeJobs() *wakeJobs {
	ctx, cancel := context.WithCancel(context.Background())
	return &wakeJobs{jobs: map[string]WakeJob{}, active: map[string]string{}, ctx: ctx, cancel: cancel, run: ExecuteWake}
}
func (j *wakeJobs) close() { j.mu.Lock(); j.closed = true; j.cancel(); j.mu.Unlock(); j.wg.Wait() }
func (j *wakeJobs) activeID(device string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.active[device]
}
func (j *wakeJobs) get(id string) (WakeJob, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	v, ok := j.jobs[id]
	if ok && v.State != "running" && time.Since(v.Updated) > 10*time.Minute {
		delete(j.jobs, id)
		return WakeJob{}, false
	}
	return v, ok
}
func (j *wakeJobs) submit(store *Store, id string) (WakeJob, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return WakeJob{}, errJobsBusy
	}
	if jobID := j.active[id]; jobID != "" {
		return j.jobs[jobID], nil
	}
	if len(j.active) >= 2 {
		return WakeJob{}, errJobsBusy
	}
	d, password, err := store.WakeSnapshot(id)
	if err != nil {
		return WakeJob{}, err
	}
	if !d.UsesVNC() {
		clear(password)
		return WakeJob{}, errors.New("该设备未配置 Mac VNC")
	}
	// Keep at most 256 snapshots; discard only completed jobs, never running ones.
	for key, v := range j.jobs {
		if v.State != "running" && time.Since(v.Updated) > 10*time.Minute {
			delete(j.jobs, key)
		}
	}
	if len(j.jobs) >= 256 {
		var oldest string
		var at time.Time
		for key, v := range j.jobs {
			if v.State != "running" && (oldest == "" || v.Updated.Before(at)) {
				oldest, at = key, v.Updated
			}
		}
		delete(j.jobs, oldest)
	}
	job := WakeJob{ID: randomToken(), DeviceID: id, State: "running", Progress: "唤醒任务已创建", Updated: time.Now()}
	j.jobs[job.ID] = job
	j.active[id] = job.ID
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		defer clear(password)
		ctx, cancel := context.WithTimeout(j.ctx, 30*time.Second+time.Duration(d.VNC.HoldSeconds)*time.Second)
		defer cancel()
		result := j.run(ctx, d, password, func(message string) {
			j.mu.Lock()
			v := j.jobs[job.ID]
			v.Progress = message
			v.Updated = time.Now()
			j.jobs[job.ID] = v
			j.mu.Unlock()
		})
		if err := store.Record(Event{time.Now().UTC().Format(time.RFC3339), d.ID, d.Name, result.Message}); err != nil {
			result.Warning = "操作记录保存失败"
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		v := j.jobs[job.ID]
		v.State = result.Status
		v.Progress = result.Message
		v.Result = result
		v.Updated = time.Now()
		j.jobs[job.ID] = v
		delete(j.active, id)
	}()
	return job, nil
}
