package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// 下发用的进程/服务名统一在这里做合法性校验，避免把用户输入直接拼进 pidof。
var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ReloadResult 描述一次重载的结果，方便前端和日志展示。
type ReloadResult struct {
	Service string `json:"service"`
	Method  string `json:"method"` // signal | reload | restart | none
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// pidOf 用 pidof 找进程；失败返回 0。
func pidOf(name string) int {
	if !safeName.MatchString(name) {
		return 0
	}
	out, err := exec.Command("/bin/pidof", name).Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) == 0 {
		return 0
	}
	for _, s := range f {
		if p, err := atoi(s); err == nil && p > 1 {
			return p
		}
	}
	return 0
}

func signalProcess(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// canWrite 判断当前用户（含 root）是否能写目标路径。
func canWrite(path string) bool {
	dir := filepath.Dir(path)
	return syscall.Access(dir, 2) == nil
}

// ReloadService 平滑重载一个服务：优先 SIGHUP（dnsmasq 会重读 addnhosts），
// 其次 /etc/init.d/<name> reload，最后才退回 restart。返回实际采用的方式。
func ReloadService(name, strategy string) ReloadResult {
	name = strings.TrimSpace(name)
	if name == "" {
		return ReloadResult{Method: "none", OK: true, Message: "无需重载"}
	}
	if !safeName.MatchString(name) {
		return ReloadResult{Service: name, Method: "none", OK: false, Message: "服务名非法: " + name}
	}
	strategies := []string{"signal", "initd", "restart"}
	if s := strings.TrimSpace(strategy); s != "" && s != "auto" {
		strategies = []string{s}
	}
	var lastMsg string
	for _, st := range strategies {
		switch st {
		case "signal":
			pid := pidOf(name)
			if pid == 0 {
				lastMsg = name + " 未在运行，跳过信号重载"
				continue
			}
			if err := signalProcess(pid, syscall.SIGHUP); err != nil {
				lastMsg = fmt.Sprintf("向 %s(%d) 发 SIGHUP 失败: %v", name, pid, err)
				continue
			}
			time.Sleep(300 * time.Millisecond)
			if pidOf(name) != 0 {
				return ReloadResult{Service: name, Method: "signal", OK: true, Message: fmt.Sprintf("已向 %s(%d) 发送 SIGHUP，未中断服务", name, pid)}
			}
			lastMsg = name + " 收到 SIGHUP 后退出，改用重载/重启"
		case "reload", "initd":
			if _, err := os.Stat("/etc/init.d/" + name); err != nil {
				lastMsg = "没有 /etc/init.d/" + name
				continue
			}
			out, err := exec.Command("/etc/init.d/"+name, "reload").CombinedOutput()
			msg := strings.TrimSpace(string(out))
			if err != nil && (strings.Contains(msg, "not supported") || strings.Contains(msg, "Usage")) {
				lastMsg = name + " 不支持 reload"
				continue
			}
			if err != nil {
				lastMsg = fmt.Sprintf("%s reload 失败: %v %s", name, err, msg)
				continue
			}
			return ReloadResult{Service: name, Method: "reload", OK: true, Message: name + " 已重载"}
		case "restart":
			if _, err := os.Stat("/etc/init.d/" + name); err != nil {
				lastMsg = "没有 /etc/init.d/" + name
				continue
			}
			out, err := exec.Command("/etc/init.d/"+name, "restart").CombinedOutput()
			if err != nil {
				lastMsg = fmt.Sprintf("%s restart 失败: %v %s", name, err, strings.TrimSpace(string(out)))
				continue
			}
			return ReloadResult{Service: name, Method: "restart", OK: true, Message: name + " 已重启"}
		}
	}
	if lastMsg == "" {
		lastMsg = "没有可用的重载方式"
	}
	return ReloadResult{Service: name, Method: "none", OK: false, Message: lastMsg}
}

// restartService 兼容旧调用：默认走平滑重载。
func restartService(name string) error {
	r := ReloadService(name, "auto")
	if !r.OK {
		return fmt.Errorf("%s", r.Message)
	}
	return nil
}

var ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// lookupIPv4 通过本机 DNS 解析域名，返回 A 记录里的 IPv4（去重、保序）。
// 用 nslookup -type=a 而不是查询 0 记录，避免解析器提前超时。
func lookupIPv4(domain, server string) ([]string, error) {
	if server == "" {
		server = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/nslookup", "-type=a", domain, server).CombinedOutput()
	txt := string(out)
	if err != nil && !ipv4Re.MatchString(txt) {
		return nil, fmt.Errorf("nslookup %s@%s 失败: %v", domain, server, err)
	}
	seen := map[string]bool{}
	var ips []string
	for _, m := range ipv4Re.FindAllString(txt, -1) {
		if seen[m] {
			continue
		}
		if net.ParseIP(m) == nil {
			continue
		}
		seen[m] = true
		ips = append(ips, m)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("未解析到 IPv4（%s）", domain)
	}
	return ips, nil
}

// VerifyDomain 校验域名当前是否解析到期望集合里的 IP。
func VerifyDomain(domain string, expect []string, server string) (bool, []string, string) {
	got, err := lookupIPv4(domain, server)
	if err != nil {
		return false, nil, err.Error()
	}
	set := map[string]bool{}
	for _, e := range expect {
		set[e] = true
	}
	var hit []string
	for _, g := range got {
		if set[g] {
			hit = append(hit, g)
		}
	}
	if len(hit) == 0 {
		return false, got, "当前解析结果与本次优选 IP 不一致"
	}
	return true, got, ""
}
