package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

type Server struct {
	eng      *Engine
	cm       *ConfigManager
	lastCron string
}

func NewServer(eng *Engine, cm *ConfigManager) *Server {
	return &Server{eng: eng, cm: cm}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

func ok(w http.ResponseWriter, extra map[string]any) {
	if extra == nil {
		extra = map[string]any{}
	}
	extra["ok"] = true
	writeJSON(w, extra)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"version": Version})
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cm.Get()
		snap := s.eng.Latest()
		summary := map[string]any{}
		if snap != nil {
			summary = map[string]any{
				"time":        snap.Time,
				"count":       len(snap.Results),
				"duration_ms": snap.DurationMS,
				"stats":       snap.Stats,
				"top":         topSlice(snap.Results, 10),
			}
		}
		next := ""
		if cfg.Schedule != "" {
			if c, err := ParseCron(cfg.Schedule); err == nil {
				if t, found := nextRun(c, time.Now()); found {
					next = t.Format("2006-01-02 15:04")
				}
			}
		}
		ok(w, map[string]any{
			"version":  Version,
			"progress": s.eng.Progress(),
			"latest":   summary,
			"schedule": cfg.Schedule,
			"next_run": next,
			"uptime":   int64(time.Since(startTime).Seconds()),
		})
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			ok(w, map[string]any{"config": s.cm.Get()})
		case "POST", "PUT":
			var c Config
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				writeErr(w, 400, "配置解析失败: "+err.Error())
				return
			}
			if c.Schedule != "" {
				if _, err := ParseCron(c.Schedule); err != nil {
					writeErr(w, 400, "定时表达式无效: "+err.Error())
					return
				}
			}
			if err := s.cm.Set(c); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			ok(w, map[string]any{"config": s.cm.Get()})
		default:
			writeErr(w, 405, "method not allowed")
		}
	})

	mux.HandleFunc("/api/scan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeErr(w, 405, "method not allowed")
			return
		}
		var override *Config
		if r.ContentLength > 0 {
			// 只覆盖请求里显式给出的字段，其余沿用当前配置
			var patch map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&patch); err == nil && len(patch) > 0 {
				merged := s.cm.Get()
				base, _ := json.Marshal(merged)
				var m map[string]json.RawMessage
				json.Unmarshal(base, &m)
				for k, v := range patch {
					m[k] = v
				}
				b, _ := json.Marshal(m)
				var c Config
				if json.Unmarshal(b, &c) == nil {
					override = &c
				}
			}
		}
		if err := s.eng.Start(override); err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		ok(w, nil)
	})

	mux.HandleFunc("/api/scan/stop", func(w http.ResponseWriter, r *http.Request) {
		s.eng.Stop()
		ok(w, nil)
	})

	mux.HandleFunc("/api/results", func(w http.ResponseWriter, r *http.Request) {
		snap := s.eng.Latest()
		if snap == nil {
			ok(w, map[string]any{"results": []Result{}, "time": ""})
			return
		}
		limit := intParam(r, "limit", 100)
		ok(w, map[string]any{"time": snap.Time, "stats": snap.Stats, "duration_ms": snap.DurationMS, "results": topSlice(snap.Results, limit)})
	})

	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"logs": s.eng.Logs()})
	})

	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("file")
		if name == "" {
			ok(w, map[string]any{"history": s.eng.History()})
			return
		}
		snap, err := s.eng.HistoryFile(name)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		ok(w, map[string]any{"snapshot": snap})
	})

	mux.HandleFunc("/api/export", func(w http.ResponseWriter, r *http.Request) {
		snap := s.eng.Latest()
		if snap == nil {
			writeErr(w, 404, "还没有扫描结果")
			return
		}
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "text"
		}
		topN := intParam(r, "topn", s.cm.Get().TopN)
		out, err := Export(snap.Results, format, topN)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(out))
	})

	mux.HandleFunc("/api/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeErr(w, 405, "method not allowed")
			return
		}
		snap := s.eng.Latest()
		if snap == nil || len(snap.Results) == 0 {
			writeErr(w, 404, "还没有扫描结果")
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		cfg := s.cm.Get()
		var msgs []string
		applied := false
		for _, t := range cfg.ApplyTargets {
			if req.Name != "" && t.Name != req.Name {
				continue
			}
			if !t.Enabled && req.Name == "" {
				continue
			}
			applied = true
			if err := ApplyTo(t, snap.Results, cfg.TopN); err != nil {
				go s.eng.logf("下发 [%s] 失败: %v", t.Name, err)
				writeErr(w, 500, fmt.Sprintf("[%s] %v", t.Name, err))
				return
			}
			msgs = append(msgs, t.Name+" 已更新")
			go s.eng.logf("下发 [%s] 成功（域名 %s，Top%d）", t.Name, t.Domain, cfg.TopN)
		}
		if !applied {
			writeErr(w, 400, "没有启用的下发目标")
			return
		}
		ok(w, map[string]any{"messages": msgs})
	})

	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IP   string `json:"ip"`
			Port int    `json:"port"`
			Full bool   `json:"full"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
			writeErr(w, 400, "请提供 IP")
			return
		}
		if req.Port == 0 {
			req.Port = 443
		}
		cfg := s.cm.Get()
		addr := net.JoinHostPort(req.IP, fmt.Sprint(req.Port))
		timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
		res := Result{IP: req.IP, Port: req.Port, Addr: addr}
		var sum time.Duration
		var cnt int
		for i := 0; i < cfg.Probes; i++ {
			if d, err := tcpPing(addr, timeout); err == nil {
				sum += d
				cnt++
				if res.LatencyMS == 0 || float64(d.Microseconds())/1000 < res.LatencyMS {
					res.LatencyMS = float64(d.Microseconds()) / 1000
				}
			}
		}
		if cnt == 0 {
			writeErr(w, 200, "TCP 连接失败（IP 可能不通）")
			return
		}
		res.AvgMS = float64(sum.Microseconds()) / 1000 / float64(cnt)
		res.LossPct = float64(cfg.Probes-cnt) / float64(cfg.Probes) * 100
		if d, err := tlsPing(addr, cfg.SNI, timeout*3); err == nil {
			res.TLSMS = float64(d.Microseconds()) / 1000
		}
		if colo, err := fetchColo(addr, cfg.SNI, cfg.TestHost, timeout*4); err == nil {
			res.Colo = colo
		}
		if mbps, ttfb, n, err := measureSpeed(addr, cfg.SNI, cfg.TestHost, cfg.DownBytes, time.Duration(cfg.TimeoutMS*10)*time.Millisecond); err == nil && n > 0 {
			res.SpeedMbps = mbps
			res.TTFBMS = float64(ttfb.Microseconds()) / 1000
		}
		ok(w, map[string]any{"result": res})
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	return mux
}

func topSlice(rs []Result, n int) []Result {
	if n <= 0 || n > len(rs) {
		n = len(rs)
	}
	return rs[:n]
}

func intParam(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func nextRun(c *Cron, from time.Time) (time.Time, bool) {
	t := from.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 60*24*366; i++ {
		if c.Match(t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

var startTime = time.Now()

// Run 启动 HTTP 服务与定时调度。
func (s *Server) Run(addr string) error {
	if addr == "" {
		addr = s.cm.Get().Listen
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go s.scheduler()
	log.Printf("cf-yxipd %s 监听 %s，配置 %s", Version, addr, s.cm.path)
	return srv.ListenAndServe()
}

func (s *Server) scheduler() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cfg := s.cm.Get()
		if cfg.Schedule == "" {
			continue
		}
		c, err := ParseCron(cfg.Schedule)
		if err != nil {
			continue
		}
		now := time.Now()
		key := now.Format("200601021504")
		if c.Match(now) && s.lastCron != key {
			s.lastCron = key
			s.eng.logf("定时任务触发（%s）", cfg.Schedule)
			if err := s.eng.Start(nil); err != nil {
				s.eng.logf("定时任务启动失败: %v", err)
			}
		}
	}
}

var _ = os.Getpid
