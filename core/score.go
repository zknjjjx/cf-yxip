package main

// ScoreResults 按权重给结果打分（0-100）：速度越高、延迟越低、丢包越少得分越高。
func ScoreResults(rs []Result, cfg Config) {
	if len(rs) == 0 {
		return
	}
	var maxSpeed float64
	for _, r := range rs {
		if r.SpeedMbps > maxSpeed {
			maxSpeed = r.SpeedMbps
		}
	}
	total := cfg.WeightSpeed + cfg.WeightLatency + cfg.WeightLoss
	if total <= 0 {
		total = 1
	}
	for i := range rs {
		r := &rs[i]
		// 延迟分：0ms 满分，cfg.MaxLatencyMS 及以上 0 分
		lat := 1 - r.AvgMS/float64(cfg.MaxLatencyMS)
		if lat < 0 {
			lat = 0
		}
		// 速度分：相对本轮最快者归一化；没有测速数据时用延迟分兜底
		var spd float64
		if maxSpeed > 0 {
			spd = r.SpeedMbps / maxSpeed
		} else {
			spd = lat
		}
		loss := 1 - r.LossPct/100
		score := (cfg.WeightSpeed*spd + cfg.WeightLatency*lat + cfg.WeightLoss*loss) / total * 100
		if r.TLSMS > 0 {
			// TLS 握手明显异常（>1.5s）的降权
			if r.TLSMS > 1500 {
				score *= 0.7
			}
		}
		r.Score = float64(int(score*10+0.5)) / 10
	}
}
