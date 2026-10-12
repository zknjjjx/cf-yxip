package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronField struct {
	any bool
	set map[int]bool
}

type Cron struct {
	fields [5]cronField
	raw    string
}

func parseCronField(s string, min, max int) (cronField, error) {
	f := cronField{set: map[int]bool{}}
	if s == "*" {
		f.any = true
		return f, nil
	}
	for _, part := range strings.Split(s, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n < 1 {
				return f, fmt.Errorf("bad step %q", part)
			}
			step = n
			part = part[:i]
		}
		lo, hi := min, max
		if part != "*" {
			if i := strings.Index(part, "-"); i >= 0 {
				a, err1 := strconv.Atoi(part[:i])
				b, err2 := strconv.Atoi(part[i+1:])
				if err1 != nil || err2 != nil {
					return f, fmt.Errorf("bad range %q", part)
				}
				lo, hi = a, b
			} else {
				v, err := strconv.Atoi(part)
				if err != nil {
					return f, fmt.Errorf("bad value %q", part)
				}
				lo, hi = v, v
				if step > 1 {
					hi = max
				}
			}
		}
		if lo < min || hi > max || lo > hi {
			return f, fmt.Errorf("out of range %q", part)
		}
		for v := lo; v <= hi; v += step {
			f.set[v] = true
		}
	}
	return f, nil
}

// ParseCron 解析标准 5 段 cron 表达式：分 时 日 月 周
func ParseCron(expr string) (*Cron, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron 需要 5 段（分 时 日 月 周），当前 %d 段", len(fields))
	}
	ranges := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	c := &Cron{raw: expr}
	for i := 0; i < 5; i++ {
		f, err := parseCronField(fields[i], ranges[i][0], ranges[i][1])
		if err != nil {
			return nil, fmt.Errorf("第 %d 段: %v", i+1, err)
		}
		c.fields[i] = f
	}
	return c, nil
}

func (c *Cron) Match(t time.Time) bool {
	vals := [5]int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	for i := 0; i < 5; i++ {
		if c.fields[i].any {
			continue
		}
		if i == 4 && t.Weekday() == time.Sunday && c.fields[i].set[7] {
			continue
		}
		if !c.fields[i].set[vals[i]] {
			return false
		}
	}
	return true
}

func (c *Cron) String() string { return c.raw }
