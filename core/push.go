package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// FilterResults 按配置里的阈值丢弃不达标的结果。
// 阈值一律 0 = 不限，这样用户可以只放开需要的那一项。
func FilterResults(rs []Result, cfg Config) []Result {
	out := make([]Result, 0, len(rs))
	for _, r := range rs {
		if cfg.MaxLatencyMS > 0 && r.AvgMS > float64(cfg.MaxLatencyMS) {
			continue
		}
		if cfg.MinLatencyMS > 0 && r.AvgMS < float64(cfg.MinLatencyMS) {
			continue
		}
		if cfg.MaxLossPct > 0 && r.LossPct > cfg.MaxLossPct {
			continue
		}
		if cfg.MinSpeedMbps > 0 && cfg.Mode != "tcp" && r.SpeedMbps < cfg.MinSpeedMbps {
			// tcp 模式没有测速数据，速度项不参与判断；full 模式测不出速度的按不达标处理
			continue
		}
		out = append(out, r)
	}
	return out
}

// resultName 生成给 cfnew 之类下游看的节点名，例如 LAX-90ms。
func resultName(r Result) string {
	return fmt.Sprintf("%s-%.0fms", coloTag(r), r.AvgMS)
}

// coloTag 取结果所属机房标签，拿不到时用 CF 兜底。
func coloTag(r Result) string {
	colo := strings.TrimSpace(r.Colo)
	if colo == "" {
		colo = "CF"
	}
	return colo
}

func httpDoJSON(client *http.Client, method, url string, body any, headers map[string]string) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return out, resp.StatusCode, nil
}

// applyGitHub 把 Top N 结果写进 GitHub 仓库里的一个文件；cfnew 之类的下游可以直接
// 把这个文件的 raw 地址填进「优选IP来源URL」，格式必须形如 IP:端口#名称。
func applyGitHub(t ApplyTarget, rs []Result, topN int) error {
	repo := strings.TrimSpace(t.Repo)
	if repo == "" {
		return fmt.Errorf("缺少 GitHub 仓库（owner/name）")
	}
	path := strings.Trim(strings.TrimSpace(t.Path), "/")
	if path == "" {
		path = "cf-ip.txt"
	}
	branch := strings.TrimSpace(t.Branch)
	if branch == "" {
		branch = "main"
	}
	format := strings.TrimSpace(t.Format)
	if format == "" {
		format = "cfnew"
	}
	token := strings.TrimSpace(t.Token)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	if token == "" {
		return fmt.Errorf("缺少 GitHub 令牌（目标里填，或给服务配置 GH_TOKEN）")
	}
	n := topN
	if t.TopN > 0 {
		n = t.TopN
	}
	content, err := Export(rs, format, n)
	if err != nil {
		return err
	}
	api := "https://api.github.com/repos/" + repo + "/contents/" + path
	hd := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/vnd.github+json",
		"User-Agent":    "cf-yxip/" + Version,
	}
	client := &http.Client{Timeout: 30 * time.Second}

	sha := ""
	out, code, err := httpDoJSON(client, "GET", api+"?ref="+branch, nil, hd)
	if err != nil {
		return fmt.Errorf("读取仓库文件失败: %v", err)
	}
	if code == 200 {
		var cur struct {
			SHA string `json:"sha"`
		}
		if json.Unmarshal(out, &cur) == nil {
			sha = cur.SHA
		}
	} else if code != 404 {
		return fmt.Errorf("读取仓库文件 HTTP %d: %s", code, strings.TrimSpace(string(out)))
	}

	payload := map[string]any{
		"message": fmt.Sprintf("cf-yxip 更新优选 IP（%s）", time.Now().Format("2006-01-02 15:04")),
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  branch,
	}
	if sha != "" {
		payload["sha"] = sha
	}
	out, code, err = httpDoJSON(client, "PUT", api, payload, hd)
	if err != nil {
		return fmt.Errorf("推送 GitHub 失败: %v", err)
	}
	if code != 200 && code != 201 {
		return fmt.Errorf("GitHub 返回 HTTP %d: %s", code, strings.TrimSpace(string(out)))
	}
	return nil
}

// applyCFnew 直接调 cfnew 的优选 IP 接口写入 KV（需要在面板里开启「允许API管理」）。
func applyCFnew(t ApplyTarget, rs []Result, topN int) error {
	base := strings.TrimRight(strings.TrimSpace(t.URL), "/")
	if base == "" {
		return fmt.Errorf("缺少 cfnew 地址，例如 https://cfnew.example.com")
	}
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	auth := strings.Trim(strings.TrimSpace(t.Path), "/")
	if auth == "" {
		return fmt.Errorf("缺少认证路径（cfnew 的自定义路径或 UUID）")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	ep := base + "/" + auth + "/api/preferred-ips"

	if t.Replace {
		out, code, err := httpDoJSON(client, "DELETE", ep, map[string]any{"all": true}, nil)
		if err != nil {
			return fmt.Errorf("清空 cfnew 旧列表失败: %v", err)
		}
		if code != 200 {
			return fmt.Errorf("清空 cfnew 旧列表 HTTP %d: %s", code, strings.TrimSpace(string(out)))
		}
	}

	n := topN
	if t.TopN > 0 {
		n = t.TopN
	}
	if n > len(rs) {
		n = len(rs)
	}
	items := make([]map[string]any, 0, n)
	for _, r := range rs[:n] {
		items = append(items, map[string]any{
			"ip":   r.IP,
			"port": r.Port,
			"name": resultName(r),
		})
	}
	out, code, err := httpDoJSON(client, "POST", ep, items, nil)
	if err != nil {
		return fmt.Errorf("提交到 cfnew 失败: %v", err)
	}
	if code != 200 {
		return fmt.Errorf("cfnew 返回 HTTP %d: %s", code, strings.TrimSpace(string(out)))
	}
	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	json.Unmarshal(out, &resp)
	if !resp.Success {
		return fmt.Errorf("cfnew 未接受：%s %s", resp.Error, resp.Message)
	}
	return nil
}
