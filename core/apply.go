package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Export 把结果导出成各种下游工具能直接吃的格式。
func Export(rs []Result, format string, topN int) (string, error) {
	if topN <= 0 {
		topN = 10
	}
	if topN > len(rs) {
		topN = len(rs)
	}
	sub := rs[:topN]
	var b strings.Builder
	switch format {
	case "", "text":
		for _, r := range sub {
			fmt.Fprintln(&b, r.IP)
		}
	case "addr":
		for _, r := range sub {
			fmt.Fprintln(&b, r.Addr)
		}
	case "cdn":
		for _, r := range sub {
			fmt.Fprintf(&b, "%s#cf-yxip %.0fms\n", r.Addr, r.AvgMS)
		}
	case "csv":
		fmt.Fprintln(&b, "ip,port,avg_ms,loss_pct,tls_ms,speed_mbps,colo,score")
		for _, r := range sub {
			fmt.Fprintf(&b, "%s,%d,%.1f,%.0f,%.1f,%.2f,%s,%.1f\n", r.IP, r.Port, r.AvgMS, r.LossPct, r.TLSMS, r.SpeedMbps, r.Colo, r.Score)
		}
	case "json":
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		enc.Encode(sub)
	case "ipset":
		for _, r := range sub {
			fmt.Fprintf(&b, "add cf_yxip %s\n", r.IP)
		}
	case "hosts":
		for _, r := range sub {
			fmt.Fprintf(&b, "%s\t%s\n", r.IP, r.Colo)
		}
	default:
		return "", fmt.Errorf("不支持的格式: %s", format)
	}
	return b.String(), nil
}

// DnsmasqConf 生成 dnsmasq 的 address 改写规则，多个 IP 会形成轮询应答。
func DnsmasqConf(domain string, rs []Result, topN int) string {
	if topN > len(rs) {
		topN = len(rs)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# cf-yxip 自动生成 %s\n", time.Now().Format("2006-01-02 15:04:05"))
	for _, r := range rs[:topN] {
		fmt.Fprintf(&b, "address=/%s/%s\t# %.0fms", domain, r.IP, r.AvgMS)
		if r.SpeedMbps > 0 {
			fmt.Fprintf(&b, " %.2fMbps", r.SpeedMbps)
		}
		b.WriteString("\n")
	}
	return b.String()
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ApplyTo 把 Top N 结果下发到一个目标（dnsmasq / hosts / AdGuard Home）。
func ApplyTo(t ApplyTarget, rs []Result, defTopN int) error {
	if t.Domain == "" {
		return fmt.Errorf("缺少域名")
	}
	topN := t.TopN
	if topN <= 0 {
		topN = defTopN
	}
	if len(rs) == 0 {
		return fmt.Errorf("没有可用结果")
	}
	switch t.Type {
	case "dnsmasq":
		path := t.Path
		if path == "" {
			path = "/etc/dnsmasq.d/cf-yxip.conf"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(DnsmasqConf(t.Domain, rs, topN)), 0644); err != nil {
			return err
		}
		return restartService(t.Restart)
	case "hosts":
		path := t.Path
		if path == "" {
			path = "/etc/cf-yxip/hosts"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# cf-yxip %s\n", time.Now().Format("2006-01-02 15:04:05"))
		for _, r := range rs[:min(topN, len(rs))] {
			fmt.Fprintf(&b, "%s\t%s\n", r.IP, t.Domain)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
			return err
		}
		return restartService(t.Restart)
	case "adguard":
		return applyAdGuard(t, rs, topN)
	default:
		return fmt.Errorf("未知目标类型: %s", t.Type)
	}
}

func restartService(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if !safeName.MatchString(name) {
		return fmt.Errorf("服务名非法: %s", name)
	}
	out, err := exec.Command("/etc/init.d/"+name, "restart").CombinedOutput()
	if err != nil {
		return fmt.Errorf("重启 %s 失败: %v %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyAdGuard 通过 AdGuard Home 的 API 改写 DNS 记录：先删掉同域名的旧记录，再写入新的。
func applyAdGuard(t ApplyTarget, rs []Result, topN int) error {
	base := strings.TrimRight(t.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req := func(method, path string, body any) ([]byte, int, error) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		r, err := http.NewRequest(method, base+path, rd)
		if err != nil {
			return nil, 0, err
		}
		if t.User != "" || t.Pass != "" {
			r.SetBasicAuth(t.User, t.Pass)
		}
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(r)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return out, resp.StatusCode, nil
	}

	// 删除旧的同域名记录
	if out, code, err := req("GET", "/control/rewrite/list", nil); err == nil && code == 200 {
		var list []struct {
			Domain string `json:"domain"`
			Answer string `json:"answer"`
		}
		if json.Unmarshal(out, &list) == nil {
			for _, rw := range list {
				if !strings.EqualFold(rw.Domain, t.Domain) {
					continue
				}
				req("POST", "/control/rewrite/delete", map[string]string{"domain": rw.Domain, "answer": rw.Answer})
			}
		}
	} else if err != nil {
		return fmt.Errorf("连接 AdGuard Home 失败: %v", err)
	}

	n := min(topN, len(rs))
	for _, r := range rs[:n] {
		if _, code, err := req("POST", "/control/rewrite/add", map[string]string{"domain": t.Domain, "answer": r.IP}); err != nil {
			return err
		} else if code != 200 {
			return fmt.Errorf("AdGuard 返回 HTTP %d", code)
		}
	}
	return nil
}

// NotifyWebhook 扫描完成后推送一条摘要到 webhook（飞书/钉钉/自建均可）。
func NotifyWebhook(url string, snap *Snapshot) error {
	n := 10
	if len(snap.Results) < n {
		n = len(snap.Results)
	}
	var lines []string
	for i, r := range snap.Results[:n] {
		lines = append(lines, fmt.Sprintf("%d. %s  %.0fms %.2fMbps %s", i+1, r.Addr, r.AvgMS, r.SpeedMbps, r.Colo))
	}
	title := fmt.Sprintf("cf-yxip 优选完成：%d 个可用 / 用时 %.0fs", len(snap.Results), float64(snap.DurationMS)/1000)
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": title + "\n" + strings.Join(lines, "\n")},
	}
	b, _ := json.Marshal(payload)
	cl := &http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = strconv.Itoa
