package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Result struct {
	IP        string  `json:"ip"`
	Port      int     `json:"port"`
	Addr      string  `json:"addr"`
	LatencyMS float64 `json:"latency_ms"`
	AvgMS     float64 `json:"avg_ms"`
	JitterMS  float64 `json:"jitter_ms"`
	LossPct   float64 `json:"loss_pct"`
	TLSMS     float64 `json:"tls_ms"`
	TTFBMS    float64 `json:"ttfb_ms"`
	SpeedMbps float64 `json:"speed_mbps"`
	Colo      string  `json:"colo"`
	Score     float64 `json:"score"`
	Timestamp int64   `json:"ts"`
}

type ScanStats struct {
	Targets     int   `json:"targets"`
	Alive       int   `json:"alive"`
	Benchmarked int   `json:"benchmarked"`
	DurationMS  int64 `json:"duration_ms"`
	SourceCount int   `json:"sources"`
}

type Snapshot struct {
	Time       string    `json:"time"`
	DurationMS int64     `json:"duration_ms"`
	Config     Config    `json:"config"`
	Stats      ScanStats `json:"stats"`
	Results    []Result  `json:"results"`
}

type Progress struct {
	Running   bool    `json:"running"`
	Phase     string  `json:"phase"`
	Total     int     `json:"total"`
	Done      int     `json:"done"`
	Alive     int     `json:"alive"`
	Tested    int     `json:"tested"`
	Pct       float64 `json:"pct"`
	Elapsed   float64 `json:"elapsed"`
	StartedAt string  `json:"started_at"`
	Message   string  `json:"message"`
}

// Engine 管理扫描任务、进度、历史结果。
type Engine struct {
	sink     func(string)
	mu       sync.Mutex
	cm       *ConfigManager
	stateDir string

	prog    Progress
	started time.Time
	cancel  context.CancelFunc
	logs    []LogLine
	latest  *Snapshot
}

type LogLine struct {
	T   string `json:"t"`
	Msg string `json:"msg"`
}

func NewEngine(cm *ConfigManager, stateDir string) *Engine {
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	e := &Engine{cm: cm, stateDir: stateDir}
	os.MkdirAll(filepath.Join(stateDir, "snapshots"), 0755)
	e.loadLatest()
	return e
}

// SetLogSink 让调用方（例如 CLI）能实时看到日志。
func (e *Engine) SetLogSink(f func(string)) {
	e.mu.Lock()
	e.sink = f
	e.mu.Unlock()
}

func (e *Engine) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	e.mu.Lock()
	sink := e.sink
	e.mu.Unlock()
	if sink != nil {
		sink(msg)
	}
	e.mu.Lock()
	e.logs = append(e.logs, LogLine{T: time.Now().Format("15:04:05"), Msg: msg})
	if len(e.logs) > 400 {
		e.logs = e.logs[len(e.logs)-400:]
	}
	e.mu.Unlock()
}

func (e *Engine) Logs() []LogLine {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]LogLine, len(e.logs))
	copy(out, e.logs)
	return out
}

func (e *Engine) Progress() Progress {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.prog
	if p.Running {
		p.Elapsed = time.Since(e.started).Seconds()
	}
	return p
}

func (e *Engine) Latest() *Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.latest
}

func (e *Engine) loadLatest() {
	b, err := os.ReadFile(filepath.Join(e.stateDir, "latest.json"))
	if err != nil {
		return
	}
	var s Snapshot
	if json.Unmarshal(b, &s) == nil {
		e.latest = &s
	}
}

func (e *Engine) saveSnapshot(s *Snapshot) error {
	os.MkdirAll(filepath.Join(e.stateDir, "snapshots"), 0755)
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	name := time.Now().Format("20060102-150405") + ".json"
	if err := os.WriteFile(filepath.Join(e.stateDir, "snapshots", name), b, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(e.stateDir, "latest.json"), b, 0644); err != nil {
		return err
	}
	keep := e.cm.Get().SnapshotKeep
	entries, _ := os.ReadDir(filepath.Join(e.stateDir, "snapshots"))
	var files []string
	for _, en := range entries {
		if !en.IsDir() && filepath.Ext(en.Name()) == ".json" {
			files = append(files, en.Name())
		}
	}
	if len(files) > keep {
		sort.Strings(files)
		for _, f := range files[:len(files)-keep] {
			os.Remove(filepath.Join(e.stateDir, "snapshots", f))
		}
	}
	return nil
}

func (e *Engine) History() []map[string]any {
	entries, _ := os.ReadDir(filepath.Join(e.stateDir, "snapshots"))
	out := []map[string]any{}
	for _, en := range entries {
		if en.IsDir() || filepath.Ext(en.Name()) != ".json" {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"file": en.Name(), "size": info.Size(), "mtime": info.ModTime().Format("2006-01-02 15:04:05")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["file"].(string) > out[j]["file"].(string) })
	return out
}

func (e *Engine) HistoryFile(name string) (*Snapshot, error) {
	if filepath.Base(name) != name {
		return nil, fmt.Errorf("非法文件名")
	}
	b, err := os.ReadFile(filepath.Join(e.stateDir, "snapshots", name))
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Start 启动一次扫描（异步）。
func (e *Engine) Start(override *Config) error {
	e.mu.Lock()
	if e.prog.Running {
		e.mu.Unlock()
		return fmt.Errorf("已有扫描任务在运行")
	}
	cfg := e.cm.Get()
	if override != nil {
		cfg = *override
		cfg.Normalize()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.started = time.Now()
	e.prog = Progress{Running: true, Phase: "collect", StartedAt: e.started.Format("15:04:05"), Message: "正在收集 IP 段"}
	e.mu.Unlock()

	go func() {
		snap, err := e.run(ctx, cfg)
		e.mu.Lock()
		e.prog.Running = false
		e.cancel = nil
		if err != nil {
			e.prog.Phase = "error"
			e.prog.Message = err.Error()
		} else {
			e.prog.Phase = "done"
			e.prog.Pct = 100
			e.prog.Message = fmt.Sprintf("完成：%d 个可用，用时 %.1fs", len(snap.Results), float64(snap.DurationMS)/1000)
			e.latest = snap
		}
		e.mu.Unlock()
		if err == nil {
			if serr := e.saveSnapshot(snap); serr != nil {
				e.logf("保存结果失败: %v", serr)
			} else {
				e.logf("结果已保存（%d 条）", len(snap.Results))
			}
			go e.afterRun(&cfg, snap)
		}
	}()
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
		e.prog.Message = "已手动停止"
	}
}

func (e *Engine) run(ctx context.Context, cfg Config) (*Snapshot, error) {
	t0 := time.Now()
	e.logf("开始扫描：模式=%s 端口=%v 并发=%d 超时=%dms 探测=%d次", cfg.Mode, cfg.Ports, cfg.Concurrency, cfg.TimeoutMS, cfg.Probes)

	targets := CollectTargets(cfg, e.logf)
	if len(targets) == 0 {
		return nil, fmt.Errorf("没有可扫描的目标 IP，请检查 IP 源或自定义 IP")
	}
	e.logf("目标总数 %d", len(targets))
	e.mu.Lock()
	e.prog.Total = len(targets)
	e.prog.Phase = "tcp"
	e.mu.Unlock()

	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	jobs := make(chan Target)
	var mu sync.Mutex
	var alive []Result
	var done int
	var wg sync.WaitGroup

	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if ctx.Err() != nil {
					return
				}
				res := Result{IP: t.IP, Port: t.Port, Addr: t.Addr(), Timestamp: time.Now().Unix()}
				var ok int
				var sum time.Duration
				min := time.Hour
				max := time.Duration(0)
				for p := 0; p < cfg.Probes; p++ {
					d, err := tcpPing(t.Addr(), timeout)
					if err == nil {
						ok++
						sum += d
						if d < min {
							min = d
						}
						if d > max {
							max = d
						}
					}
				}
				if ok > 0 {
					res.LatencyMS = float64(min.Microseconds()) / 1000
					res.AvgMS = float64(sum.Microseconds()) / 1000 / float64(ok)
					res.JitterMS = float64((max - min).Microseconds()) / 1000
					res.LossPct = float64(cfg.Probes-ok) / float64(cfg.Probes) * 100
					if res.LatencyMS <= float64(cfg.MaxLatencyMS) {
						mu.Lock()
						alive = append(alive, res)
						mu.Unlock()
					}
				}
				mu.Lock()
				done++
				if done%20 == 0 || done == len(targets) {
					e.mu.Lock()
					e.prog.Done = done
					e.prog.Alive = len(alive)
					e.prog.Pct = float64(done) / float64(len(targets)) * 45
					e.mu.Unlock()
				}
				mu.Unlock()
			}
		}()
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		jobs <- t
	}
	close(jobs)
	wg.Wait()

	if ctx.Err() != nil {
		return nil, fmt.Errorf("扫描已停止")
	}
	sort.Slice(alive, func(i, j int) bool { return alive[i].AvgMS < alive[j].AvgMS })
	e.logf("TCP 阶段结束：%d/%d 可用", len(alive), len(targets))
	if len(alive) == 0 {
		return nil, fmt.Errorf("没有可用 IP（可放宽延迟上限或增加探测次数）")
	}

	e.mu.Lock()
	e.prog.Phase = "bench"
	e.prog.Alive = len(alive)
	e.prog.Pct = 45
	e.mu.Unlock()

	if cfg.Mode == "full" {
		k := cfg.TopK
		if k > len(alive) {
			k = len(alive)
		}
		cand := alive[:k]
		e.logf("进入深度测试：前 %d 个候选（TLS + 机房识别 + 下载测速）", k)
		var wg2 sync.WaitGroup
		sem := make(chan struct{}, cfg.SpeedConc)
		var tmu sync.Mutex
		tested := 0
		for i := range cand {
			wg2.Add(1)
			go func(idx int) {
				defer wg2.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				r := &cand[idx]
				if d, err := tlsPing(r.Addr, cfg.SNI, timeout*3); err == nil {
					r.TLSMS = float64(d.Microseconds()) / 1000
				}
				if colo, err := fetchColo(r.Addr, cfg.SNI, cfg.TestHost, timeout*4); err == nil {
					r.Colo = colo
				}
				if mbps, ttfb, n, err := measureSpeed(r.Addr, cfg.SNI, cfg.TestHost, cfg.DownBytes, time.Duration(cfg.TimeoutMS*10)*time.Millisecond); err == nil && n > 0 {
					r.SpeedMbps = mbps
					r.TTFBMS = float64(ttfb.Microseconds()) / 1000
				}
				tmu.Lock()
				tested++
				e.mu.Lock()
				e.prog.Tested = tested
				e.prog.Pct = 45 + float64(tested)/float64(k)*55
				e.mu.Unlock()
				tmu.Unlock()
			}(i)
		}
		wg2.Wait()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("扫描已停止")
		}
		alive = cand
		e.logf("深度测试完成，开始评分排序")
	}

	ScoreResults(alive, cfg)
	sort.Slice(alive, func(i, j int) bool {
		if alive[i].Score == alive[j].Score {
			return alive[i].AvgMS < alive[j].AvgMS
		}
		return alive[i].Score > alive[j].Score
	})

	// 按用户设置的阈值（延迟上下限 / 速度下限 / 丢包上限）过滤
	beforeFilter := len(alive)
	alive = FilterResults(alive, cfg)
	if dropped := beforeFilter - len(alive); dropped > 0 {
		e.logf("阈值过滤：丢弃 %d 个不达标结果，剩余 %d 个", dropped, len(alive))
	}
	if len(alive) == 0 {
		return nil, fmt.Errorf("结果全部被阈值过滤掉了，请放宽延迟/速度/丢包阈值")
	}

	snap := &Snapshot{
		Time:       time.Now().Format("2006-01-02 15:04:05"),
		DurationMS: time.Since(t0).Milliseconds(),
		Config:     cfg,
		Stats: ScanStats{
			Targets:     len(targets),
			Alive:       len(alive),
			Benchmarked: e.Progress().Tested,
			DurationMS:  time.Since(t0).Milliseconds(),
			SourceCount: len(cfg.Sources),
		},
		Results: alive,
	}
	e.logf("扫描完成，用时 %.1fs", float64(snap.DurationMS)/1000)
	return snap, nil
}

// afterRun 在扫描结束后做自动下发与通知。
func (e *Engine) afterRun(cfg *Config, snap *Snapshot) {
	if cfg.AutoApply {
		for _, t := range cfg.ApplyTargets {
			if !t.Enabled {
				continue
			}
			r, err := ApplyToTarget(t, snap.Results, cfg.TopN)
			if err != nil {
				e.logf("自动下发 [%s] 失败: %v", t.Name, err)
				continue
			}
			if r.Reload.Message != "" {
				e.logf("自动下发 [%s] 成功（%s）", t.Name, r.Reload.Message)
			} else {
				e.logf("自动下发 [%s] 成功", t.Name)
			}
			if r.Checked && r.OK {
				e.logf("自动下发 [%s] 自检通过：%s", t.Name, r.Message)
			}
		}
	}
	if cfg.NotifyWebhook != "" {
		if err := NotifyWebhook(cfg.NotifyWebhook, snap); err != nil {
			e.logf("通知推送失败: %v", err)
		} else {
			e.logf("通知推送成功")
		}
	}
}
