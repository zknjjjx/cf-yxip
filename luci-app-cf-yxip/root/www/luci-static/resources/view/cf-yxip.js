'use strict';
'require view';
'require fs';
'require ui';

/*
 * luci-app-cf-yxip 前端：Cloudflare 优选 IP
 * 后端 cf-yxipd 监听 127.0.0.1:7790，本文件通过 /usr/libexec/cf-yxip 壳脚本调用。
 * 标签页：首页 / 结果 / 下发 / 历史 / 设置 / 日志
 */

var BIN = '/usr/libexec/cf-yxip';
var POLL_MS = 2500;

function run(args) {
	return fs.exec(BIN, args).then(function (r) {
		var out = ((r.stdout || '') + '').trim();
		try { return JSON.parse(out); }
		catch (e) {
			var msg = ((r.stderr || '') + out).trim();
			throw new Error(msg || ('后端无输出 (exit ' + r.code + ')'));
		}
	});
}
function runRaw(args) {
	return fs.exec(BIN, args).then(function (r) { return (r.stdout || '') + ''; });
}

function notify(msg, type) { ui.addNotification(null, E('p', {}, msg), type || 'info'); }
function fail(e) { ui.hideModal(); notify((e && e.message) ? e.message : String(e), 'danger'); }
function wait(msg) { ui.showModal(null, [E('p', { 'class': 'spinning' }, msg)]); }

function btn(label, cls, fn, title) {
	return E('button', { 'class': 'btn cbi-button ' + (cls || ''), 'title': title || '', 'click': ui.createHandlerFn(null, fn) }, label);
}
function txt(v, ph) { return E('input', { 'type': 'text', 'value': (v === undefined || v === null) ? '' : v, 'placeholder': ph || '' }); }
function numIn(v, step) { return E('input', { 'type': 'number', 'value': (v === undefined || v === null) ? '' : v, 'step': step || 1 }); }
function ck(v) { return E('input', { 'type': 'checkbox', 'checked': v ? true : null }); }
function sel(opts, v) {
	return E('select', {}, (opts || []).map(function (o) {
		return E('option', { 'value': o[0], 'selected': String(v) === String(o[0]) ? true : null }, o[1]);
	}));
}
function ta(v, rows) {
	var s = Array.isArray(v) ? v.join('\n') : (v || '');
	return E('textarea', { 'rows': rows || 3, 'spellcheck': 'false' }, s);
}
function card(title, body, right) {
	return E('div', { 'class': 'cyx-card' }, [E('h4', {}, [E('span', {}, title), right || '']), E('div', {}, body)]);
}
function field(label, input, hint) {
	return E('div', { 'class': 'cyx-field' }, [E('label', {}, label), input, hint ? E('div', { 'class': 'cyx-hint' }, hint) : '']);
}
function badge(text, kind, title) { return E('span', { 'class': 'cyx-badge cyx-' + kind, 'title': title || '' }, text); }
function fmtDur(sec) {
	sec = Math.max(0, Math.round(sec || 0));
	var p = function (n) { return (n < 10 ? '0' : '') + n; };
	return p(Math.floor(sec / 3600)) + ':' + p(Math.floor(sec % 3600 / 60)) + ':' + p(sec % 60);
}
function fmtMS(v) { return (v && v > 0) ? (v.toFixed(0) + ' ms') : '-'; }
function kv(pairs) {
	return E('div', { 'class': 'cyx-kv' }, pairs.filter(Boolean).map(function (p) {
		return E('span', {}, [E('b', {}, p[0]), p[1]]);
	}));
}

var CSS = [
	'.cyx-tabs{display:flex;flex-wrap:wrap;gap:4px;border-bottom:1px solid rgba(128,128,128,.25);margin:10px 0 16px}',
	'.cyx-tabs a{padding:8px 16px;cursor:pointer;border-bottom:2px solid transparent}',
	'.cyx-tabs a.on{border-bottom-color:#09c;font-weight:600}',
	'.cyx-card{border:1px solid rgba(128,128,128,.25);border-radius:6px;padding:12px 14px;margin-bottom:14px}',
	'.cyx-card>h4{margin:0 0 10px;display:flex;align-items:center;justify-content:space-between;gap:10px;font-size:15px}',
	'.cyx-badge{display:inline-block;padding:1px 8px;border-radius:10px;font-size:12px;border:1px solid currentColor}',
	'.cyx-ok{color:#2e7d32}.cyx-warn{color:#e08600}.cyx-bad{color:#c62828}.cyx-off{color:#777}',
	'.cyx-field{margin-bottom:10px}.cyx-field>label{display:block;font-weight:600;margin-bottom:3px;font-size:13px}',
	'.cyx-field input[type=text],.cyx-field input[type=number],.cyx-field select,.cyx-field textarea{width:100%;max-width:560px;box-sizing:border-box}',
	'.cyx-hint{font-size:12px;opacity:.7;margin-top:2px}',
	'.cyx-bar{height:14px;border-radius:7px;background:rgba(128,128,128,.2);overflow:hidden;margin:6px 0}',
	'.cyx-bar>i{display:block;height:100%;background:#09c;width:0;transition:width .3s}',
	'.cyx-tbl{width:100%;border-collapse:collapse;font-size:13px}',
	'.cyx-tbl th,.cyx-tbl td{border-bottom:1px solid rgba(128,128,128,.2);padding:5px 8px;text-align:left;white-space:nowrap}',
	'.cyx-tbl tbody tr:hover{background:rgba(128,128,128,.08)}',
	'.cyx-mono{font-family:monospace;font-size:12px;white-space:pre-wrap;max-height:480px;overflow:auto;background:rgba(0,0,0,.05);padding:10px;border-radius:6px}',
	'.cyx-ops{display:flex;flex-wrap:wrap;gap:8px;align-items:center}',
	'.cyx-kv{display:flex;flex-wrap:wrap;gap:6px 22px;font-size:13px;margin-bottom:8px}',
	'.cyx-kv span b{font-weight:600;margin-right:3px}',
	'.cyx-tgt{border:1px dashed rgba(128,128,128,.4);border-radius:6px;padding:10px 12px;margin-bottom:10px}'
].join('\n');

var CFG_FIELDS = [
	['listen', '监听地址', 'text', '默认 127.0.0.1:7790（仅本机）'],
	['mode', '扫描模式', 'select:full=full：连通+TLS+测速（准，较慢）|tcp=tcp：只测连通与延迟（快）', ''],
	['concurrency', '并发数', 'int', 'N1 建议 128 左右'],
	['sample_per_cidr', '每个网段采样 IP 数', 'int', ''],
	['max_targets', '最大探测目标数', 'int', ''],
	['ports', '端口（逗号分隔）', 'csvint', ''],
	['timeout_ms', '单次超时（毫秒）', 'int', ''],
	['probes', '每个 IP 探测次数', 'int', ''],
	['max_latency_ms', '延迟上限（毫秒）', 'int', '超过此值的结果丢弃'],
	['min_latency_ms', '延迟下限（毫秒）', 'int', '低于此值的结果丢弃，0 = 不限'],
	['min_speed_mbps', '速度下限（Mbps）', 'float', '低于此值的结果丢弃，0 = 不限；tcp 模式没有测速数据，此项不参与判断'],
	['max_loss_pct', '丢包上限（%）', 'float', '高于此值的结果丢弃，0 = 不限'],
	['topk', '进入测速的 IP 数', 'int', ''],
	['topn', '默认输出条数', 'int', ''],
	['test_host', '测速/取 Colo 域名', 'text', '默认 speed.cloudflare.com'],
	['sni', 'TLS SNI', 'text', '留空用上面的域名'],
	['down_bytes', '测速下载字节数', 'int', '默认 3000000（约 3MB）'],
	['speed_concurrency', '测速并发', 'int', ''],
	['weight_speed', '速度权重', 'float', ''],
	['weight_latency', '延迟权重', 'float', ''],
	['weight_loss', '丢包权重', 'float', ''],
	['snapshot_keep', '历史快照保留份数', 'int', ''],
	['schedule', '定时任务（cron 5 段）', 'text', '留空关闭。例：0 4 * * * 每天 04:00 扫描'],
	['notify_webhook', '结果推送 Webhook', 'text', '留空关闭；可填飞书/钉钉机器人地址'],
	['auto_apply', '扫描完成后自动下发', 'bool', ''],
	['sources', 'IP 来源（每行一个 URL）', 'lines', '默认 Cloudflare 官方 ipv4/ipv6 列表'],
	['custom_ips', '自定义 IP/CIDR（每行一个）', 'lines', '填了就优先只扫这些']
];

return view.extend({
	load: function () {
		return run(['status']).catch(function (e) { return { ok: false, error: e.message }; });
	},

	render: function (st) {
		this.st = st || {};
		this.tab = this.tab || 'home';
		this.nav = E('div', { 'class': 'cyx-tabs' });
		this.content = E('div');
		this.renderNav();
		this.show(this.tab);
		this.poll();
		return E('div', { 'class': 'cbi-map' }, [
			E('style', {}, CSS),
			E('h2', {}, 'CF 优选IP'),
			E('div', { 'class': 'cbi-map-descr' }, '优选 Cloudflare 边缘 IP：测延迟/丢包/速度，挑最快的写进 dnsmasq、hosts 或 AdGuard Home。'),
			this.nav,
			this.content
		]);
	},

	renderNav: function () {
		var self = this;
		var tabs = [['home', '首页'], ['results', '结果'], ['apply', '下发'], ['history', '历史'], ['config', '设置'], ['logs', '日志']];
		this.nav.innerHTML = '';
		tabs.forEach(function (t) {
			self.nav.appendChild(E('a', {
				'class': self.tab === t[0] ? 'on' : '',
				'click': ui.createHandlerFn(null, function () { self.show(t[0]); })
			}, t[1]));
		});
	},

	show: function (tab) {
		var self = this;
		this.tab = tab;
		this.renderNav();
		this.content.innerHTML = '';
		this.content.appendChild(E('div', { 'class': 'cyx-hint' }, '加载中…'));
		var fn = {
			home: 'renderHome', results: 'renderResults', apply: 'renderApply',
			history: 'renderHistory', config: 'renderConfig', logs: 'renderLogs'
		}[tab];
		this[fn](true).catch(fail);
	},

	poll: function () {
		var self = this;
		if (this.timer) clearInterval(this.timer);
		this.timer = setInterval(function () {
			if (self.tab !== 'home' && self.tab !== 'logs') return;
			var fn = self.tab === 'home' ? 'renderHome' : 'renderLogs';
			self[fn](true).catch(function () { });
		}, POLL_MS);
	},

	resultTable: function (rs, limit) {
		rs = (rs || []).slice(0, limit || 0);
		if (!rs.length) return E('div', { 'class': 'cyx-hint' }, '暂无结果');
		var head = E('tr', {}, ['#', '地址', '平均延迟', '丢包', 'TLS', '速度', 'Colo', '得分'].map(function (h) { return E('th', {}, h); }));
		var body = rs.map(function (r, i) {
			return E('tr', {}, [
				E('td', {}, String(i + 1)),
				E('td', { 'class': 'cyx-mono' }, r.addr || (r.ip + ':' + r.port)),
				E('td', {}, fmtMS(r.avg_ms)),
				E('td', {}, (r.loss_pct || 0).toFixed(0) + ' %'),
				E('td', {}, fmtMS(r.tls_ms)),
				E('td', {}, r.speed_mbps ? r.speed_mbps.toFixed(2) + ' Mbps' : '-'),
				E('td', {}, r.colo || '-'),
				E('td', {}, (r.score || 0).toFixed(1))
			]);
		});
		return E('table', { 'class': 'cyx-tbl' }, [E('thead', {}, [head]), E('tbody', {}, body)]);
	},

	renderHome: function () {
		var self = this;
		return run(['status']).then(function (st) {
			self.st = st;
			self.content.innerHTML = '';
			if (st.ok === false) {
				self.content.appendChild(card('服务状态', [
					kv([['状态', badge('未运行', 'bad', st.error || '')]]),
					E('div', { 'class': 'cyx-hint' }, st.error || 'cf-yxipd 没有响应'),
					E('div', { 'class': 'cyx-ops' }, [
						btn('启动服务', 'cbi-button-apply', function () { self.svc('start'); }),
						btn('查看日志', 'cbi-button-neutral', function () { self.show('logs'); })
					])
				]));
				return;
			}
			var p = st.progress || {}, latest = st.latest || {}, running = !!p.running;
			self.content.appendChild(card('服务状态', [
				kv([
					['状态', badge('运行中', 'ok', 'cf-yxipd 正常')],
					['版本', st.version || '-'],
					['已运行', fmtDur(st.uptime)],
					['定时任务', st.schedule ? st.schedule : '未设置'],
					st.next_run ? ['下次运行', st.next_run] : null
				]),
				E('div', { 'class': 'cyx-ops' }, [
					running
						? btn('停止扫描', 'cbi-button-reset', function () { self.act('stop'); })
						: btn('立即扫描', 'cbi-button-apply', function () { self.act('scan'); }),
					btn('重启服务', 'cbi-button-neutral', function () { self.svc('restart'); }),
					btn('停止服务', 'cbi-button-reset', function () { self.svc('stop'); }, '停止 cf-yxipd：面板会显示未运行'),
					btn('刷新', 'cbi-button-neutral', function () { self.show('home'); })
				])
			]));

			if (running) {
				var pct = Math.max(0, Math.min(100, p.pct || 0));
				self.content.appendChild(card('正在扫描', [
					kv([
						['阶段', p.phase || '-'],
						['进度', (p.done || 0) + ' / ' + (p.total || 0)],
						['存活', p.alive || 0],
						['已测速', p.tested || 0],
						['用时', fmtDur(p.elapsed)]
					]),
					E('div', { 'class': 'cyx-bar' }, [E('i', { 'style': 'width:' + pct + '%' })]),
					E('div', { 'class': 'cyx-hint' }, p.message || '')
				]));
			}

			if (latest && latest.count) {
				self.content.appendChild(card('最近一次结果', [
					kv([
						['时间', latest.time || '-'],
						['可用 IP', latest.count + ' 个'],
						['本次用时', ((latest.duration_ms || 0) / 1000).toFixed(1) + ' s'],
						['探测目标', (latest.stats ? latest.stats.targets : '-') + ' 个']
					]),
					self.resultTable(latest.top || [], 10),
					E('div', { 'class': 'cyx-ops' }, [
						btn('全部结果 / 导出', 'cbi-button-neutral', function () { self.show('results'); }),
						btn('下发到已启用目标', 'cbi-button-apply', function () { self.applyNow(''); })
					])
				]));
			}
			else {
				self.content.appendChild(card('最近一次结果', [E('div', { 'class': 'cyx-hint' }, '还没有结果，点上面的「立即扫描」跑一次。')]));
			}
		});
	},

	act: function (kind) {
		var self = this;
		var p;
		if (kind === 'scan') p = run(['scan', '{}']).then(function (r) { if (r.ok === false) throw new Error(r.error); notify('扫描已开始，首页可看进度'); });
		else if (kind === 'stop') p = run(['stop']).then(function () { notify('已请求停止扫描'); });
		else p = run(['health']).then(function (r) { notify('后端连通：' + JSON.stringify(r)); });
		wait('执行中…');
		return p.then(function () { ui.hideModal(); return self.show(self.tab); }).catch(fail);
	},

	svc: function (action) {
		var self = this;
		wait('正在' + action + '服务…');
		return run(['svc', action]).then(function () {
			ui.hideModal();
			notify('已执行：' + action);
			setTimeout(function () { self.show(self.tab); }, 1500);
		}).catch(fail);
	},

	renderResults: function () {
		var self = this;
		var limit = this.resLimit || 50;
		return run(['results', String(limit)]).then(function (res) {
			if (res.ok === false) throw new Error(res.error);
			self.content.innerHTML = '';
			var fmtSel = sel([['text', 'text：纯 IP'], ['addr', 'addr：IP:端口'], ['cfnew', 'cfnew：IP:端口#机房-延迟'], ['cfscan', 'cfscan：IP:端口#机房 速度MB/s 延迟ms'], ['cdn', 'cdn：IP:端口#延迟'], ['csv', 'csv：表格'], ['json', 'json'], ['ipset', 'ipset'], ['hosts', 'hosts']], self.exportFmt || 'csv');
			var nIn = numIn(self.exportN || ((res.results || []).length || 10), 1);
			var limSel = sel([[10, '10 条'], [20, '20 条'], [50, '50 条'], [100, '100 条'], [200, '200 条']], limit);
			limSel.addEventListener('change', function () { self.resLimit = parseInt(limSel.value, 10); self.show('results'); });
			var outBox = E('div', { 'class': 'cyx-mono', 'style': 'display:none' });
			self.content.appendChild(card('导出', [
				E('div', { 'class': 'cyx-ops' }, [
					E('span', {}, '格式'), fmtSel,
					E('span', {}, '条数'), nIn,
					btn('导出', 'cbi-button-apply', function () { self.exportNow(fmtSel.value, nIn.value, outBox); }),
					btn('下发到已启用目标', 'cbi-button-neutral', function () { self.applyNow(''); })
				]),
				E('div', { 'class': 'cyx-hint' }, 'dnsmasq 用「下发」标签页；其它 DNS/CDN 用 text 或 addr 格式复制走。'),
				outBox
			], btn('刷新', 'cbi-button-neutral', function () { self.show('results'); })));

			var f = self.resFilter || {};
			var numPh = function (v, step, ph) { return E('input', { 'type': 'number', 'step': String(step), 'placeholder': ph, 'value': (v === undefined || v === null) ? '' : v }); };
			var latIn = numPh(f.maxLat, 1, '不限');
			var spdIn = numPh(f.minSpd, 0.1, '不限');
			var lossIn = numPh(f.maxLoss, 1, '不限');
			var tblBox = E('div');
			var cnt = E('span', { 'class': 'cyx-hint' });
			var redraw = function () {
				var all = res.results || [];
				var maxLat = parseFloat(latIn.value), minSpd = parseFloat(spdIn.value), maxLoss = parseFloat(lossIn.value);
				self.resFilter = { maxLat: latIn.value, minSpd: spdIn.value, maxLoss: lossIn.value };
				var rs = all.filter(function (r) {
					if (!isNaN(maxLat) && (r.avg_ms || 0) > maxLat) return false;
					if (!isNaN(minSpd) && (r.speed_mbps || 0) < minSpd) return false;
					if (!isNaN(maxLoss) && (r.loss_pct || 0) > maxLoss) return false;
					return true;
				});
				tblBox.innerHTML = '';
				tblBox.appendChild(self.resultTable(rs, limit));
				cnt.textContent = '筛选后 ' + rs.length + ' / ' + all.length + ' 个';
			};
			[latIn, spdIn, lossIn].forEach(function (el) { el.addEventListener('input', redraw); });
			redraw();
			self.content.appendChild(card('扫描结果', [
				kv([
					['快照时间', res.time || '-'],
					['可用 IP', (res.results || []).length + ' 个'],
					['本次用时', ((res.duration_ms || 0) / 1000).toFixed(1) + ' s']
				]),
				E('div', { 'class': 'cyx-ops' }, [
					E('span', {}, '显示'), limSel,
					E('span', {}, '延迟 ≤'), latIn,
					E('span', {}, '速度 ≥'), spdIn,
					E('span', {}, '丢包 ≤'), lossIn,
					E('span', {}, '%'), cnt
				]),
				tblBox
			]));
		});
	},

	exportNow: function (fmt, topn, box) {
		this.exportFmt = fmt;
		this.exportN = parseInt(topn, 10);
		wait('导出中…');
		return runRaw(['export', fmt, String(topn)]).then(function (text) {
			ui.hideModal();
			if (text.indexOf('{"ok":false') === 0) throw new Error(text);
			box.style.display = 'block';
			box.textContent = text;
			notify('已导出 ' + fmt + ' 格式，可直接复制');
		}).catch(fail);
	},

	applyNow: function (name) {
		wait('正在下发…');
		return run(['apply', name || '']).then(function (r) {
			ui.hideModal();
			if (r.ok === false) throw new Error(r.error);
			notify('下发完成：' + (r.msgs ? r.msgs.join('；') : '成功'));
		}).catch(fail);
	},

	renderApply: function () {
		var self = this;
		return run(['config']).then(function (res) {
			if (res.ok === false) throw new Error(res.error);
			self.cfg = res.config;
			self.tgtRefs = [];
			self.content.innerHTML = '';
			var box = E('div');
			var targets = self.cfg.apply_targets || [];
			targets.forEach(function (t) { box.appendChild(self.targetEditor(t)); });
			if (!targets.length) box.appendChild(E('div', { 'class': 'cyx-hint' }, '还没有下发目标，点下面「添加目标」。'));
			self.content.appendChild(card('下发目标', [
				E('div', { 'class': 'cyx-hint' }, '扫描出的 Top N IP 会写进这些位置。dnsmasq 写成 address=/域名/IP 轮询应答；AdGuard Home 通过 API 改写 DNS；hosts 写文件；GitHub 写成仓库文件（cfnew 的「优选IP来源URL」可直接填它的 raw 地址）；cfnew 直接调它的接口写 KV。'),
				box,
				E('div', { 'class': 'cyx-ops' }, [
					btn('添加目标', 'cbi-button-add', function () {
						self.cfg.apply_targets = targets.concat([{ name: '新目标', type: 'adguard', domain: '', enabled: false, url: 'http://127.0.0.1:3000', user: '', pass: '', path: '', restart: '', topn: 0, repo: '', branch: '', format: '', token: '', replace: false }]);
						self.show('apply');
					}),
					btn('保存', 'cbi-button-apply', function () { self.saveTargets(); }),
					btn('立即下发全部启用目标', 'cbi-button-neutral', function () { self.applyNow(''); })
				])
			], btn('重新加载', 'cbi-button-neutral', function () { self.show('apply'); })));
		});
	},

	targetEditor: function (t) {
		var self = this;
		var refs = [];
		var bind = function (key, el, get) {
			refs.push({ k: key, el: el, get: get || function (e) { return e.value; } });
			return el;
		};
		var typeSel = bind('type', sel([['adguard', 'AdGuard Home'], ['dnsmasq', 'dnsmasq'], ['hosts', 'hosts 文件'], ['github', 'GitHub 仓库文件'], ['cfnew', 'CFnew（写 KV）']], t.type));
		var enabled = bind('enabled', E('input', { 'type': 'checkbox', 'checked': t.enabled ? true : null }), function (e) { return !!e.checked; });
		var nameIn = bind('name', txt(t.name, '例如 图床走 CF'));
		var box = E('div', { 'class': 'cyx-tgt' }, [
			E('div', { 'class': 'cyx-ops' }, [
				E('label', {}, [enabled, ' 启用']),
				btn('立即下发此目标', 'cbi-button-neutral', function () { self.applyNow(nameIn.value); }),
				btn('删除此目标', 'cbi-button-remove', function () {
					var i = self.tgtRefs.indexOf(refs);
					var arr = self.cfg.apply_targets || [];
					if (i >= 0) { arr.splice(i, 1); self.cfg.apply_targets = arr; }
					self.show('apply');
				})
			]),
			field('名称', nameIn),
			field('类型', typeSel, 'dnsmasq：写本地 DNS 改写文件；AdGuard：调 API 改写；hosts：写 hosts 文件；github：把结果推到 GitHub 仓库文件；cfnew：调 cfnew 的优选IP接口写 KV'),
			field('域名', bind('domain', txt(t.domain, '例如 cdn.example.com'))),
			field('地址', bind('url', txt(t.url, 'AdGuard：http://127.0.0.1:3000；cfnew：https://cfnew.007.cc.cd')), 'AdGuard Home 与 cfnew 都用这个'),
			field('AdGuard 用户名', bind('user', txt(t.user, ''))),
			field('AdGuard 密码', bind('pass', E('input', { 'type': 'password', 'value': t.pass || '' }))),
			field('路径', bind('path', txt(t.path, 'dnsmasq 默认 /etc/dnsmasq.d/cf-yxip.conf')), 'dnsmasq/hosts 写入路径；github 是仓库里的文件路径（默认 cf-ip.txt）；cfnew 填它的自定义路径或 UUID'),
			field('下发后重启的服务', bind('restart', txt(t.restart, '例如 dnsmasq，留空不重启'))),
			field('GitHub 仓库', bind('repo', txt(t.repo, 'owner/name，例如 zknjjjx/cf-ip')), 'github 类型用；文件内容形如 IP:端口#机房-延迟'),
			field('GitHub 分支', bind('branch', txt(t.branch, '默认 main'))),
			field('GitHub 格式', bind('format', sel([['cfnew', 'cfnew：IP:端口#名称'], ['cfscan', 'cfscan：IP:端口#机房 速度MB/s 延迟ms'], ['cdn', 'cdn：IP:端口#延迟'], ['addr', 'addr：IP:端口'], ['text', 'text：纯 IP']], t.format || 'cfnew'))),
			field('GitHub 令牌', bind('token', E('input', { 'type': 'password', 'value': t.token || '' })), '留空则用服务环境变量 GH_TOKEN；填了会存在本机 /etc/cf-yxip/config.json'),
			field('cfnew 先清空旧列表', bind('replace', E('input', { 'type': 'checkbox', 'checked': t.replace ? true : null }), function (e) { return !!e.checked; }), 'cfnew 类型：勾上先删掉 KV 里的旧优选IP再写入'),
			field('使用前 N 个结果', bind('topn', numIn(t.topn || 0, 1)), '0 表示用全局默认条数')
		]);
		this.tgtRefs.push(refs);
		return box;
	},

	saveTargets: function () {
		var self = this;
		var arr = (this.tgtRefs || []).map(function (refs) {
			var o = {};
			refs.forEach(function (r) { o[r.k] = r.get(r.el); });
			return o;
		});
		this.cfg.apply_targets = arr;
		wait('保存中…');
		return run(['config', JSON.stringify(this.cfg)]).then(function (r) {
			ui.hideModal();
			if (r.ok === false) throw new Error(r.error);
			notify('下发目标已保存');
			self.show('apply');
		}).catch(fail);
	},

	renderHistory: function () {
		var self = this;
		return run(['history']).then(function (res) {
			if (res.ok === false) throw new Error(res.error);
			self.content.innerHTML = '';
			var list = res.history || [];
			var tbl = list.length
				? E('table', { 'class': 'cyx-tbl' }, [
					E('thead', {}, [E('tr', {}, ['快照文件', '大小', '时间', ''].map(function (h) { return E('th', {}, h); }))]),
					E('tbody', {}, list.map(function (h) {
						return E('tr', {}, [
							E('td', { 'class': 'cyx-mono' }, h.file),
							E('td', {}, (h.size / 1024).toFixed(1) + ' KB'),
							E('td', {}, h.mtime || '-'),
							E('td', {}, btn('查看', 'cbi-button-neutral', function () { self.loadSnapshot(h.file); }))
						]);
					}))
				])
				: E('div', { 'class': 'cyx-hint' }, '还没有历史快照（每次扫描会自动存一份）。');
			self.content.appendChild(card('历史快照', [tbl], btn('刷新', 'cbi-button-neutral', function () { self.show('history'); })));
			if (self.snapBox) self.content.appendChild(self.snapBox);
		});
	},

	loadSnapshot: function (file) {
		var self = this;
		wait('载入 ' + file + '…');
		return run(['history', file]).then(function (res) {
			ui.hideModal();
			if (res.ok === false) throw new Error(res.error);
			var s = res.snapshot || {};
			self.snapBox = card('快照 ' + file, [
				kv([
					['时间', s.time || '-'],
					['可用 IP', ((s.results || []).length) + ' 个'],
					['用时', ((s.duration_ms || 0) / 1000).toFixed(1) + ' s']
				]),
				self.resultTable(s.results || [], 100)
			]);
			self.show('history');
		}).catch(fail);
	},

	renderConfig: function () {
		var self = this;
		return run(['config']).then(function (res) {
			if (res.ok === false) throw new Error(res.error);
			self.cfg = res.config;
			self.cfgRefs = [];
			self.content.innerHTML = '';
			var grid = E('div');
			CFG_FIELDS.forEach(function (f) {
				var key = f[0], label = f[1], type = f[2], hint = f[3];
				var v = self.cfg[key], el, get;
				if (type === 'int') { el = numIn(v, 1); get = function (e) { return parseInt(e.value, 10) || 0; }; }
				else if (type === 'float') { el = numIn(v, 0.05); get = function (e) { return parseFloat(e.value) || 0; }; }
				else if (type === 'bool') { el = ck(v); get = function (e) { return !!e.checked; }; }
				else if (type === 'csvint') { el = txt((v || []).join(',')); get = function (e) { return String(e.value).split(/[,\s]+/).map(function (x) { return parseInt(x, 10); }).filter(function (x) { return !isNaN(x); }); }; }
				else if (type === 'lines') { el = ta(v, 4); get = function (e) { return String(e.value).split('\n').map(function (x) { return x.trim(); }).filter(function (x) { return x.length; }); }; }
				else if (type.indexOf('select:') === 0) {
					var opts = type.slice(7).split('|').map(function (o) { var p = o.split('='); return [p[0], p[1]]; });
					el = sel(opts, v);
				}
				else { el = txt(v); }
				self.cfgRefs.push({ k: key, el: el, get: get });
				grid.appendChild(field(label, el, hint));
			});
			self.content.appendChild(card('扫描参数', [
				grid,
				E('div', { 'class': 'cyx-hint' }, '改完点保存：下次扫描与定时任务都按这份配置跑。'),
				E('div', { 'class': 'cyx-ops' }, [
					btn('保存配置', 'cbi-button-apply', function () { self.saveConfig(); }),
					btn('重新加载', 'cbi-button-neutral', function () { self.show('config'); })
				])
			]));
		});
	},

	saveConfig: function () {
		var self = this;
		var cfg = Object.assign({}, this.cfg || {});
		(this.cfgRefs || []).forEach(function (r) { cfg[r.k] = r.get(r.el); });
		cfg.apply_targets = (this.cfg && this.cfg.apply_targets) || [];
		wait('保存中…');
		return run(['config', JSON.stringify(cfg)]).then(function (r) {
			ui.hideModal();
			if (r.ok === false) throw new Error(r.error);
			notify('配置已保存');
			self.show('config');
		}).catch(fail);
	},

	renderLogs: function () {
		var self = this;
		return run(['logs']).then(function (res) {
			self.content.innerHTML = '';
			if (res.ok === false) {
				self.content.appendChild(card('运行日志', [E('div', { 'class': 'cyx-hint' }, res.error)]));
				return;
			}
			var text = (res.logs || []).map(function (l) { return l.t + '  ' + l.msg; }).join('\n');
			var pre = E('div', { 'class': 'cyx-mono' }, text || '暂无日志');
			self.content.appendChild(card('运行日志', [pre], btn('刷新', 'cbi-button-neutral', function () { self.show('logs'); })));
			pre.scrollTop = pre.scrollHeight;
		});
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
