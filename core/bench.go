package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// dialTo 返回一个忽略目标地址、强制连到指定 IP:Port 的 net.Dialer 函数。
func dialTo(target string, timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, "tcp", target)
	}
}

func tcpPing(target string, timeout time.Duration) (time.Duration, error) {
	d := &net.Dialer{Timeout: timeout}
	t0 := time.Now()
	c, err := d.Dial("tcp", target)
	el := time.Since(t0)
	if err != nil {
		return 0, err
	}
	c.Close()
	return el, nil
}

// tlsPing 测量到目标 IP 的 TLS 握手耗时（含 TCP）。
func tlsPing(target, sni string, timeout time.Duration) (time.Duration, error) {
	d := &net.Dialer{Timeout: timeout}
	t0 := time.Now()
	conn, err := tls.DialWithDialer(d, "tcp", target, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	el := time.Since(t0)
	if err != nil {
		return 0, err
	}
	conn.Close()
	return el, nil
}

func newClient(target, sni string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:       dialTo(target, timeout),
			TLSClientConfig:   &tls.Config{ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: false,
			DisableKeepAlives: true,
			MaxIdleConns:      1,
		},
	}
}

// fetchColo 读取 Cloudflare 的 /cdn-cgi/trace，得到落地机房代码（如 HKG/NRT/LAX）。
func fetchColo(target, sni, host string, timeout time.Duration) (string, error) {
	cl := newClient(target, sni, timeout)
	req, err := http.NewRequest("GET", "https://"+host+"/cdn-cgi/trace", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cf-yxip/"+Version)
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "colo="); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("no colo")
}

// measureSpeed 通过 Cloudflare 的边缘节点下载指定字节数，得到真实下行带宽（Mbps）。
// 返回带宽、首字节时间、实际收到字节数。
func measureSpeed(target, sni, host string, bytes int, timeout time.Duration) (float64, time.Duration, int64, error) {
	cl := newClient(target, sni, timeout)
	url := "https://" + host + "/__down?bytes=" + strconv.Itoa(bytes)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("User-Agent", "cf-yxip/"+Version)
	req.Header.Set("Accept", "*/*")

	t0 := time.Now()
	resp, err := cl.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, 0, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	ttfb := time.Since(t0)
	n, err := io.Copy(io.Discard, resp.Body)
	body := time.Since(t0) - ttfb
	if err != nil && n == 0 {
		return 0, ttfb, n, err
	}
	if body <= 0 {
		body = time.Since(t0)
	}
	mbps := float64(n) * 8 / body.Seconds() / 1e6
	return mbps, ttfb, n, nil
}
