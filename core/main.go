package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

type options struct {
	cfgPath  string
	stateDir string
	listen   string
	outFmt   string
	topN     int
	timeout  int
}

// register 用“当前值”作为默认值，这样第二次解析（子命令之后的参数）
// 不会把前面已经解析到的 -c/-d 重置掉。
func (o *options) register(fs *flag.FlagSet) {
	fs.StringVar(&o.cfgPath, "c", o.cfgPath, "配置文件路径")
	fs.StringVar(&o.stateDir, "d", o.stateDir, "状态目录")
	fs.StringVar(&o.listen, "listen", "", "监听地址，覆盖配置")
	fs.StringVar(&o.outFmt, "o", "text", "输出格式: text|addr|cdn|csv|json|ipset|hosts")
	fs.IntVar(&o.topN, "n", 0, "输出条数，0 表示用配置里的 topn")
	fs.IntVar(&o.timeout, "t", 0, "整体超时秒数（scan 用），0 表示不限")
}

func main() {
	o := options{cfgPath: DefaultConfigPath, stateDir: DefaultStateDir, outFmt: "text"}
	fs := flag.NewFlagSet("cf-yxip", flag.ContinueOnError)
	o.register(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	cmd := "serve"
	rest := fs.Args()
	// 允许把参数写在子命令后面：cf-yxipd scan -o csv -n 10
	if len(rest) > 0 {
		cmd = rest[0]
		if len(rest) > 1 {
			fs2 := flag.NewFlagSet("cf-yxip", flag.ContinueOnError)
			o.register(fs2)
			if err := fs2.Parse(rest[1:]); err != nil {
				os.Exit(2)
			}
		}
	}

	cm := NewConfigManager(o.cfgPath)
	eng := NewEngine(cm, o.stateDir)

	switch cmd {
	case "serve":
		srv := NewServer(eng, cm)
		if err := srv.Run(o.listen); err != nil {
			fmt.Fprintln(os.Stderr, "服务退出:", err)
			os.Exit(1)
		}
	case "scan":
		eng.SetLogSink(func(msg string) {
			fmt.Fprintln(os.Stderr, time.Now().Format("15:04:05"), msg)
		})
		// 记下开始前的快照：扫描没跑出结果时要如实报错，不能把旧结果当成新结果打印。
		prevTime, prevDur := "", int64(-1)
		if prev := eng.Latest(); prev != nil {
			prevTime, prevDur = prev.Time, prev.DurationMS
		}
		if err := eng.Start(nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		deadline := time.Time{}
		if o.timeout > 0 {
			deadline = time.Now().Add(time.Duration(o.timeout) * time.Second)
		}
		for eng.Progress().Running {
			if !deadline.IsZero() && time.Now().After(deadline) {
				eng.Stop()
				fmt.Fprintln(os.Stderr, "扫描超时，已停止")
				os.Exit(1)
			}
			time.Sleep(time.Second)
		}
		snap := eng.Latest()
		if snap == nil {
			fmt.Fprintln(os.Stderr, eng.Progress().Message)
			os.Exit(1)
		}
		if snap.Time == prevTime && snap.DurationMS == prevDur {
			fmt.Fprintln(os.Stderr, eng.Progress().Message)
			os.Exit(1)
		}
		n := o.topN
		if n <= 0 {
			n = snap.Config.TopN
		}
		out, err := Export(snap.Results, o.outFmt, n)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(out)
	case "dump":
		snap := eng.Latest()
		b, _ := json.MarshalIndent(snap, "", "  ")
		fmt.Println(string(b))
	case "version":
		fmt.Println("cf-yxipd " + Version)
	default:
		fmt.Fprintln(os.Stderr, "用法: cf-yxipd [serve|scan|dump|version] [-c 配置] [-d 状态目录] [-o 格式] [-n 条数]")
		os.Exit(1)
	}
}
