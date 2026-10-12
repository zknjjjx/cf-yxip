package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type Target struct {
	IP   string
	Port int
}

func (t Target) Addr() string { return fmt.Sprintf("%s:%d", t.IP, t.Port) }

// fetchSource 拉取一个 IP 源，同时兼容纯文本 CIDR 列表和 Cloudflare 官方 JSON。
func fetchSource(url string, timeout time.Duration) ([]string, error) {
	cl := &http.Client{Timeout: timeout}
	resp, err := cl.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	txt := strings.TrimSpace(string(body))
	if strings.HasPrefix(txt, "{") {
		var j struct {
			Result struct {
				IPv4CIDRs []string `json:"ipv4_cidrs"`
				IPv6CIDRs []string `json:"ipv6_cidrs"`
			} `json:"result"`
		}
		if json.Unmarshal(body, &j) == nil && len(j.Result.IPv4CIDRs)+len(j.Result.IPv6CIDRs) > 0 {
			return append(j.Result.IPv4CIDRs, j.Result.IPv6CIDRs...), nil
		}
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(txt))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexAny(line, " \t,;\"'"); i > 0 {
			line = strings.Trim(line[:i], "\"',")
		}
		out = append(out, line)
	}
	return out, nil
}

// samplePrefix 在一个网段内抽样，避免直接把 /13 展开成 50 万个地址。
func samplePrefix(p netip.Prefix, n int) []netip.Addr {
	p = p.Masked()
	bits := p.Addr().BitLen() - p.Bits()
	if bits <= 0 {
		return []netip.Addr{p.Addr()}
	}
	var total uint64
	huge := bits > 63
	if !huge {
		total = uint64(1) << uint(bits)
	}
	if !huge && total <= uint64(n) {
		out := make([]netip.Addr, 0, total)
		a := p.Addr()
		for i := uint64(0); i < total; i++ {
			out = append(out, a)
			a = a.Next()
		}
		return out
	}
	out := make([]netip.Addr, 0, n)
	if !huge && total <= 1<<20 {
		step := total / uint64(n)
		if step < 1 {
			step = 1
		}
		a := p.Addr()
		for i := uint64(0); i < total && len(out) < n; i++ {
			out = append(out, a)
			for s := uint64(0); s < step; s++ {
				a = a.Next()
			}
		}
		return out
	}
	// 大网段：随机掩码抽样
	base := p.Addr().As16()
	for len(out) < n {
		var r [16]byte
		if _, err := rand.Read(r[:]); err != nil {
			break
		}
		b := base
		for i := 0; i < 16; i++ {
			keepMask := ^byte(0)
			if i < p.Bits()/8 {
				keepMask = 0xff
			} else if i == p.Bits()/8 {
				keepMask = byte(0xff << uint(8-p.Bits()%8))
			} else {
				keepMask = 0
			}
			b[i] = (b[i] & keepMask) | (r[i] & ^keepMask)
		}
		out = append(out, netip.AddrFrom16(b))
	}
	return out
}

// CollectTargets 汇总所有来源，去重并限制总量。
func CollectTargets(cfg Config, logf func(string, ...any)) []Target {
	timeout := time.Duration(cfg.TimeoutMS+7000) * time.Millisecond

	var raw []string
	for _, s := range cfg.Sources {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "file://") {
			raw = append(raw, parseLines(s)...)
			continue
		}
		if strings.HasPrefix(s, "file://") {
			if b, err := readFile(strings.TrimPrefix(s, "file://")); err == nil {
				raw = append(raw, parseLines(string(b))...)
			} else {
				logf("源读取失败 %s: %v", s, err)
			}
			continue
		}
		list, err := fetchSource(s, timeout)
		if err != nil {
			logf("源拉取失败 %s: %v", s, err)
			continue
		}
		logf("源 %s 取到 %d 条", s, len(list))
		raw = append(raw, list...)
	}
	raw = append(raw, parseLines(strings.Join(cfg.CustomIPs, "\n"))...)

	seen := map[string]bool{}
	var targets []Target
	add := func(ip string, port int) {
		key := fmt.Sprintf("%s:%d", ip, port)
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, Target{IP: ip, Port: port})
	}
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		host := item
		port := cfg.Ports[0]
		if i := strings.LastIndex(item, ":"); i > 0 && !strings.Contains(item, "::") {
			if p, err := atoi(item[i+1:]); err == nil && p > 0 && p < 65536 {
				host = item[:i]
				port = p
			}
		}
		if pfx, err := netip.ParsePrefix(host); err == nil {
			ips := samplePrefix(pfx, cfg.SamplePerCIDR)
			for _, ip := range ips {
				for _, p := range cfg.Ports {
					add(ip.String(), p)
				}
			}
			continue
		}
		if a, err := netip.ParseAddr(host); err == nil {
			add(a.String(), port)
		}
	}

	if len(targets) > cfg.MaxTargets {
		step := len(targets) / cfg.MaxTargets
		if step < 1 {
			step = 1
		}
		var trimmed []Target
		for i := 0; i < len(targets); i += step {
			trimmed = append(trimmed, targets[i])
			if len(trimmed) >= cfg.MaxTargets {
				break
			}
		}
		logf("目标数 %d 超过上限，抽样为 %d", len(targets), len(trimmed))
		targets = trimmed
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Addr() < targets[j].Addr() })
	return targets
}

func parseLines(s string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }) {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func atoi(s string) (int, error) {
	var n int
	if len(s) == 0 {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0, fmt.Errorf("too big")
		}
	}
	return n, nil
}

func readFile(p string) ([]byte, error) { return osReadFile(p) }

var _ = binary.BigEndian
