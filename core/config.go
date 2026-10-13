package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const (
	Version           = "0.3.0"
	DefaultConfigPath = "/etc/cf-yxip/config.json"
	DefaultStateDir   = "/etc/cf-yxip"
)

// ApplyTarget 描述一个“结果下发目标”。
type ApplyTarget struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // dnsmasq | adguard | hosts
	Domain  string `json:"domain"`
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"`     // adguard 管理地址，如 http://127.0.0.1:3000
	User    string `json:"user,omitempty"`    // adguard 用户名
	Pass    string `json:"pass,omitempty"`    // adguard 密码
	Path    string `json:"path,omitempty"`    // dnsmasq/hosts 写入路径
	Restart string `json:"restart,omitempty"` // 下发后重启的服务
	TopN    int    `json:"topn,omitempty"`    // 使用前 N 个结果，0 表示用全局 TopN
	Repo    string `json:"repo,omitempty"`    // github：仓库 owner/name
	Branch  string `json:"branch,omitempty"`  // github：分支，默认 main
	Format  string `json:"format,omitempty"`  // github：导出格式，默认 cfnew（IP:端口#名称）
	Token   string `json:"token,omitempty"`   // github：令牌，留空读环境变量 GH_TOKEN
	Replace bool   `json:"replace,omitempty"` // cfnew：先清空旧列表再写入
	Reload  string `json:"reload,omitempty"`  // 重载方式：auto|signal|reload|restart
	// HealthCheck 下发后做 DNS 自检，失败自动回滚。指针类型是为了区分
	// “老配置里没写这个字段”（默认开启）和“用户明确关掉了”。
	HealthCheck *bool `json:"health_check,omitempty"`
}

// WantHealthCheck 返回该目标是否要下发后自检。老配置里没有这个字段时默认开启
// （指针对 nil），只有用户明确取消勾选才会是 false。
func (t ApplyTarget) WantHealthCheck() bool {
	return t.HealthCheck == nil || *t.HealthCheck
}

type Config struct {
	Listen        string        `json:"listen"`
	Sources       []string      `json:"sources"`
	CustomIPs     []string      `json:"custom_ips"`
	Ports         []int         `json:"ports"`
	SamplePerCIDR int           `json:"sample_per_cidr"`
	MaxTargets    int           `json:"max_targets"`
	Concurrency   int           `json:"concurrency"`
	TimeoutMS     int           `json:"timeout_ms"`
	Probes        int           `json:"probes"`
	MaxLatencyMS  int           `json:"max_latency_ms"`
	MinLatencyMS  int           `json:"min_latency_ms"` // 延迟下限，低于即丢弃，0=不限
	MinSpeedMbps  float64       `json:"min_speed_mbps"` // 速度下限，低于即丢弃，0=不限
	MaxLossPct    float64       `json:"max_loss_pct"`   // 丢包上限，高于即丢弃，0=不限
	TopK          int           `json:"topk"`
	Mode          string        `json:"mode"` // tcp | full
	SNI           string        `json:"sni"`
	TestHost      string        `json:"test_host"`
	DownBytes     int           `json:"down_bytes"`
	SpeedConc     int           `json:"speed_concurrency"`
	WeightSpeed   float64       `json:"weight_speed"`
	WeightLatency float64       `json:"weight_latency"`
	WeightLoss    float64       `json:"weight_loss"`
	TopN          int           `json:"topn"`
	Schedule      string        `json:"schedule"`
	SnapshotKeep  int           `json:"snapshot_keep"`
	AutoApply     bool          `json:"auto_apply"`
	NotifyWebhook string        `json:"notify_webhook"`
	CheckRetries  int           `json:"check_retries"` // 自检重试次数（每次间隔 2s）
	CheckServer   string        `json:"check_server"`  // 自检用的 DNS 服务器，默认 127.0.0.1
	ApplyTargets  []ApplyTarget `json:"apply_targets"`
}

func DefaultConfig() Config {
	return Config{
		Listen:        "127.0.0.1:7790",
		Sources:       []string{"https://www.cloudflare.com/ips-v4", "https://www.cloudflare.com/ips-v6"},
		Ports:         []int{443},
		SamplePerCIDR: 256,
		MaxTargets:    3000,
		Concurrency:   128,
		TimeoutMS:     1000,
		Probes:        3,
		MaxLatencyMS:  600,
		TopK:          60,
		Mode:          "full",
		SNI:           "speed.cloudflare.com",
		TestHost:      "speed.cloudflare.com",
		DownBytes:     3000000,
		SpeedConc:     4,
		WeightSpeed:   0.5,
		WeightLatency: 0.35,
		WeightLoss:    0.15,
		TopN:          10,
		Schedule:      "",
		SnapshotKeep:  30,
		AutoApply:     false,
		CheckRetries:  3,
		CheckServer:   "127.0.0.1",
		ApplyTargets:  []ApplyTarget{},
	}
}

// Normalize 修正非法/越界取值，保证扫描不会因为用户填错而失控。
func (c *Config) Normalize() {
	d := DefaultConfig()
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if len(c.Ports) == 0 {
		c.Ports = d.Ports
	}
	for i, p := range c.Ports {
		if p <= 0 || p > 65535 {
			c.Ports[i] = 443
		}
	}
	if c.SamplePerCIDR < 1 {
		c.SamplePerCIDR = d.SamplePerCIDR
	}
	if c.SamplePerCIDR > 65536 {
		c.SamplePerCIDR = 65536
	}
	if c.MaxTargets < 1 || c.MaxTargets > 200000 {
		c.MaxTargets = d.MaxTargets
	}
	if c.Concurrency < 1 || c.Concurrency > 2048 {
		c.Concurrency = d.Concurrency
	}
	if c.TimeoutMS < 100 || c.TimeoutMS > 30000 {
		c.TimeoutMS = d.TimeoutMS
	}
	if c.Probes < 1 || c.Probes > 20 {
		c.Probes = d.Probes
	}
	if c.MaxLatencyMS < 10 || c.MaxLatencyMS > 60000 {
		c.MaxLatencyMS = d.MaxLatencyMS
	}
	if c.MinLatencyMS < 0 || c.MinLatencyMS > 60000 {
		c.MinLatencyMS = 0
	}
	if c.MinSpeedMbps < 0 || c.MinSpeedMbps > 10000 {
		c.MinSpeedMbps = 0
	}
	if c.MaxLossPct < 0 || c.MaxLossPct > 100 {
		c.MaxLossPct = 0
	}
	if c.TopK < 1 || c.TopK > 5000 {
		c.TopK = d.TopK
	}
	if c.Mode != "tcp" && c.Mode != "full" {
		c.Mode = d.Mode
	}
	if c.TestHost == "" {
		c.TestHost = d.TestHost
	}
	if c.SNI == "" {
		c.SNI = c.TestHost
	}
	if c.DownBytes < 100000 || c.DownBytes > 100000000 {
		c.DownBytes = d.DownBytes
	}
	if c.SpeedConc < 1 || c.SpeedConc > 32 {
		c.SpeedConc = d.SpeedConc
	}
	if c.TopN < 1 || c.TopN > 1000 {
		c.TopN = d.TopN
	}
	if c.SnapshotKeep < 1 || c.SnapshotKeep > 1000 {
		c.SnapshotKeep = d.SnapshotKeep
	}
	if c.CheckRetries < 1 || c.CheckRetries > 10 {
		c.CheckRetries = d.CheckRetries
	}
	if c.CheckServer == "" {
		c.CheckServer = d.CheckServer
	}
	for i := range c.ApplyTargets {
		switch c.ApplyTargets[i].Reload {
		case "", "auto", "signal", "reload", "restart":
		default:
			c.ApplyTargets[i].Reload = "auto"
		}
		if c.ApplyTargets[i].HealthCheck == nil {
			def := true
			c.ApplyTargets[i].HealthCheck = &def
		}
	}
	if c.WeightSpeed < 0 {
		c.WeightSpeed = d.WeightSpeed
	}
	if c.WeightLatency < 0 {
		c.WeightLatency = d.WeightLatency
	}
	if c.WeightLoss < 0 {
		c.WeightLoss = d.WeightLoss
	}
	if c.WeightSpeed+c.WeightLatency+c.WeightLoss <= 0 {
		c.WeightSpeed, c.WeightLatency, c.WeightLoss = d.WeightSpeed, d.WeightLatency, d.WeightLoss
	}
}

type ConfigManager struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func NewConfigManager(path string) *ConfigManager {
	if path == "" {
		path = DefaultConfigPath
	}
	cm := &ConfigManager{path: path, cfg: DefaultConfig()}
	if b, err := os.ReadFile(path); err == nil {
		var c Config
		if json.Unmarshal(b, &c) == nil {
			cm.cfg = c
		}
	}
	cm.cfg.Normalize()
	SetCheckOptions(cm.cfg.CheckRetries, cm.cfg.CheckServer)
	return cm
}

// SetCheckOptions 记录当前自检参数，供 apply.go 使用。
func SetCheckOptions(retries int, server string) {
	if retries <= 0 {
		retries = 3
	}
	if server == "" {
		server = "127.0.0.1"
	}
	curRetries, curServer = retries, server
}

var curRetries, curServer = 3, "127.0.0.1"

func (cm *ConfigManager) Get() Config {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cfg
}

func (cm *ConfigManager) Set(c Config) error {
	c.Normalize()
	cm.mu.Lock()
	cm.cfg = c
	cm.mu.Unlock()
	SetCheckOptions(c.CheckRetries, c.CheckServer)
	return cm.Save()
}

func (cm *ConfigManager) Save() error {
	cm.mu.RLock()
	b, err := json.MarshalIndent(cm.cfg, "", "  ")
	cm.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cm.path), 0755); err != nil {
		return err
	}
	tmp := cm.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, cm.path)
}
