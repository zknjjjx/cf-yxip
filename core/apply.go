package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	case "cfnew":
		// cfnew 的「优选IP来源URL」只认 IP:端口#名称 这种行
		for _, r := range sub {
			fmt.Fprintf(&b, "%s#%s\n", r.Addr, resultName(r))
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

// ApplyResult 记录一次下发的细节，供 API / LuCI 显示。
type ApplyResult struct {
	Target   string       `json:"target"`
	Type     string       `json:"type"`
	Domain   string       `json:"domain"`
	Count    int          `json:"count"`
	Detail   string       `json:"detail,omitempty"` // 文件路径 / 仓库 / 接口
	Reload   ReloadResult `json:"reload,omitempty"` // 服务重载结果
	Checked  bool         `json:"checked"`          // 是否做了下发后自检
	OK       bool         `json:"ok"`               // 自检是否通过
	Rollback bool         `json:"rollback"`         // 是否回滚到旧配置
	Message  string       `json:"message,omitempty"`
}

// ApplyTo 兼容旧签名：只关心成败。
func ApplyTo(t ApplyTarget, rs []Result, defTopN int) error {
	_, err := ApplyToTarget(t, rs, defTopN)
	return err
}

// ApplyToTarget 把 Top N 结果下发到一个目标（dnsmasq / hosts / AdGuard Home / GitHub / cfnew），
// 本地文件类目标在写完后会做一次 DNS 自检，失败则自动回滚旧配置。
func ApplyToTarget(t ApplyTarget, rs []Result, defTopN int) (ApplyResult, error) {
	res := ApplyResult{Target: t.Name, Type: t.Type, Domain: t.Domain}
	topN := t.TopN
	if topN <= 0 {
		topN = defTopN
	}
	if len(rs) == 0 {
		return res, fmt.Errorf("没有可用结果")
	}
	if topN > len(rs) {
		topN = len(rs)
	}
	res.Count = topN
	switch t.Type {
	case "github":
		err := applyGitHub(t, rs, topN)
		res.Detail = t.Repo + "/" + t.Path
		return res, err
	case "cfnew":
		err := applyCFnew(t, rs, topN)
		res.Detail = t.URL
		return res, err
	}
	if t.Domain == "" {
		return res, fmt.Errorf("缺少域名")
	}
	switch t.Type {
	case "dnsmasq":
		path := t.Path
		if path == "" {
			path = "/etc/dnsmasq.d/cf-yxip.conf"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return res, err
		}
		body := DnsmasqConf(t.Domain, rs, topN)
		detail, err := writeAndCheck(t, path, body, rs, topN, &res)
		res.Detail = detail
		return res, err
	case "hosts":
		path := t.Path
		if path == "" {
			path = "/etc/cf-yxip/hosts"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return res, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# cf-yxip %s\n", time.Now().Format("2006-01-02 15:04:05"))
		for _, r := range rs[:topN] {
			fmt.Fprintf(&b, "%s\t%s\n", r.IP, t.Domain)
		}
		detail, err := writeAndCheck(t, path, b.String(), rs, topN, &res)
		res.Detail = detail
		return res, err
	case "adguard":
		err := applyAdGuard(t, rs, topN)
		res.Detail = t.URL
		if err != nil {
			return res, err
		}
		if t.WantHealthCheck() {
			res.Checked = true
			if ok, got, msg := verifyWithRetry(t.Domain, rs[:topN]); ok {
				res.OK = true
				res.Message = "自检通过，解析到 " + strings.Join(got, ", ")
			} else {
				res.OK = false
				res.Rollback = true
				res.Message = "自检失败（" + msg + "），已尝试清除刚写入的 AdGuard 记录"
				rollbackAdGuard(t, rs, topN)
			}
		}
		return res, nil
	default:
		return res, fmt.Errorf("未知目标类型: %s", t.Type)
	}
}

// writeAndCheck 写本地 DNS 文件：写前留备份 -> 重载服务 -> DNS 自检 -> 失败自动回滚。
func writeAndCheck(t ApplyTarget, path, body string, rs []Result, topN int, res *ApplyResult) (string, error) {
	bak := path + ".bak"
	hasOld := false
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(bak, old, 0644); err == nil {
			hasOld = true
		}
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		return path, err
	}
	res.Reload = ReloadService(t.Restart, t.Reload)
	if t.WantHealthCheck() {
		res.Checked = true
		ok, got, msg := verifyWithRetry(t.Domain, rs[:topN])
		if !ok {
			if hasOld {
				if err := os.WriteFile(path, mustRead(bak), 0644); err != nil {
					return path, fmt.Errorf("自检失败（%s）且回滚失败: %v", msg, err)
				}
				res.Reload = ReloadService(t.Restart, t.Reload)
				res.Rollback = true
				return path, fmt.Errorf("自检失败：%s，已回滚到上一份配置", msg)
			}
			res.Rollback = true
			os.Remove(path)
			res.Reload = ReloadService(t.Restart, t.Reload)
			return path, fmt.Errorf("自检失败：%s，已删除新写入的配置（此前无可用备份）", msg)
		}
		res.OK = true
		res.Message = "自检通过，解析到 " + strings.Join(got, ", ")
	}
	if !res.Reload.OK && t.Restart != "" {
		return path, fmt.Errorf("写入成功但重载失败: %s", res.Reload.Message)
	}
	return path, nil
}

func mustRead(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

// verifyWithRetry 解析域名并与期望 IP 比对，最多试 checkRetries 次。
func verifyWithRetry(domain string, expect []Result) (bool, []string, string) {
	want := make([]string, 0, len(expect))
	for _, r := range expect {
		want = append(want, r.IP)
	}
	n := curRetries
	if n < 1 {
		n = 1
	}
	var got []string
	var msg string
	for i := 0; i < n; i++ {
		ok, g, m := VerifyDomain(domain, want, curServer)
		if ok {
			return true, g, ""
		}
		got, msg = g, m
		if i < n-1 {
			time.Sleep(2 * time.Second)
		}
	}
	return false, got, msg
}

// rollbackAdGuard 自检失败时清掉刚写进 AdGuard 的改写记录。
func rollbackAdGuard(t ApplyTarget, rs []Result, topN int) {
	base := strings.TrimRight(t.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	client := &http.Client{Timeout: 10 * time.Second}
	n := topN
	if n > len(rs) {
		n = len(rs)
	}
	for _, r := range rs[:n] {
		b, _ := json.Marshal(map[string]string{"domain": t.Domain, "answer": r.IP})
		req, err := http.NewRequest("POST", base+"/control/rewrite/delete", bytes.NewReader(b))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if t.User != "" || t.Pass != "" {
			req.SetBasicAuth(t.User, t.Pass)
		}
		if resp, err := client.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
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
