#!/usr/bin/ucode
// Dashboard watcher: runs once a minute (from /etc/init.d/dash-watch).
// Keeps 24 h of history, per-device rates, top processes and an event log.
'use strict';
import * as fs from 'fs';
import * as ubus from 'ubus';
function round(x) { return x == null ? null : int(x < 0 ? x - 0.5 : x + 0.5); }   // ucode has no round()

const RUN = '/tmp/dash', PER = '/etc/dash', MAXH = 1440;
fs.mkdir(RUN); fs.mkdir(PER);

function rj(p, d) { let s = fs.readfile(p); if (!s) return d; try { return json(s); } catch (e) { return d; } }
function wj(p, v) { let t = p + '.tmp'; fs.writefile(t, sprintf('%J', v)); fs.rename(t, p); }
function sh(c) { let p = fs.popen(c); if (!p) return ''; let s = p.read('all'); p.close(); return s || ''; }
function rd(p) { let s = fs.readfile(p); return s ? trim(s) : null; }

let now = time();
let st = rj(RUN + '/state.json', null) || rj(PER + '/state.json', null) || {};
if (!st.hist) st.hist = {};
let events = rj(PER + '/events.json', []);
let devdb = rj(PER + '/devices.json', {});
let evAdded = false, devChanged = false;

function addh(name, row) {
	let a = st.hist[name] || [];
	push(a, row);
	if (length(a) > MAXH) a = slice(a, length(a) - MAXH);
	st.hist[name] = a;
}
function ev(level, text) {
	push(events, { t: now, level: level, text: text });
	if (length(events) > 150) events = slice(events, length(events) - 150);
	evAdded = true;
}
function ping(host, n) {
	let out = sh(sprintf('ping -c %d -W 1 -q %s 2>/dev/null', n, host));
	let lm = match(out, /(\d+)% packet loss/), am = match(out, /= [0-9.]+\/([0-9.]+)\//);
	return { loss: lm ? +lm[1] : 100, avg: am ? +am[1] : null };
}

let u = ubus.connect();

// ---- latency / internet state ----
let inet = ping('1.1.1.1', 3);
if (inet.loss == 100) { let b = ping('8.8.8.8', 2); if (b.loss < 100) inet = b; }
let gw = ping('192.168.1.1', 2);
addh('lat', [now, inet.avg, inet.loss, gw.avg, gw.loss]);

let online = inet.loss < 100;
if (st.online == null) st.online = online;
if (online != st.online) {
	if (!online) { st.downSince = now; ev('bad', gw.loss < 100 ? 'Internet down (landlord router reachable)' : 'Internet down (uplink Wi-Fi/router unreachable)'); }
	else { let m = st.downSince ? round((now - st.downSince) / 60.0) : 0; ev('good', sprintf('Internet back after %d min', m)); st.downSince = null; }
	st.online = online;
}

// ---- CPU, memory, temps, WAN throughput, signal, conntrack ----
let cl = split(split(fs.readfile('/proc/stat') || '', '\n')[0], /\s+/);
let tot = 0; for (let i = 1; i < length(cl); i++) tot += +cl[i];
let idle = +cl[4] + +cl[5], cpu = null;
if (st.cpuPrev && tot > st.cpuPrev[0]) cpu = round(100.0 * (1 - (idle - st.cpuPrev[1]) / (1.0 * (tot - st.cpuPrev[0]))));
st.cpuPrev = [tot, idle];

let mem = null;
try { let si = u.call('system', 'info'); mem = round(100.0 * (si.memory.total - si.memory.available) / si.memory.total); } catch (e) {}

let tmax = null;
for (let z in fs.glob('/sys/class/thermal/thermal_zone*')) { let v = +rd(z + '/temp') / 1000.0; if (tmax == null || v > tmax) tmax = v; }
if (tmax != null) {
	if (tmax >= 85 && !st.hot) { ev('bad', sprintf('Router is hot: %d °C', tmax)); st.hot = true; }
	else if (tmax < 80 && st.hot) { ev('good', sprintf('Router cooled down: %d °C', tmax)); st.hot = false; }
}

let rx = +rd('/sys/class/net/phy0-sta0/statistics/rx_bytes'), tx = +rd('/sys/class/net/phy0-sta0/statistics/tx_bytes');
let rxr = null, txr = null;
if (st.wanPrev && now > st.wanPrev[0] && rx >= st.wanPrev[1]) {
	rxr = round((rx - st.wanPrev[1]) / (1.0 * (now - st.wanPrev[0]))); txr = round((tx - st.wanPrev[2]) / (1.0 * (now - st.wanPrev[0])));
}
st.wanPrev = [now, rx, tx];

let sig = null;
try { sig = u.call('iwinfo', 'info', { device: 'phy0-sta0' }).signal; } catch (e) {}
let ct = +rd('/proc/sys/net/netfilter/nf_conntrack_count');
addh('sys', [now, cpu, mem, tmax, sig, ct]);
addh('wan', [now, rxr, txr]);

let up = rd('/sys/class/net/phy0-sta0/operstate') != 'down';
if (st.uplink == null) st.uplink = up;
if (up != st.uplink) { ev(up ? 'good' : 'bad', up ? 'Uplink Wi-Fi reconnected' : 'Uplink Wi-Fi lost'); st.uplink = up; }

let px = rd('/tmp/proxy_upnp');
if (px && st.proxy && px != st.proxy) ev(px == 'ok' ? 'good' : 'warn', px == 'ok' ? 'Roaming proxy port forward restored' : 'Roaming proxy port forward failing');
st.proxy = px;
let pub = rd('/tmp/public_ip');
if (pub && st.pub && pub != st.pub) ev('warn', 'Public IP changed: ' + st.pub + ' → ' + pub + ' (iPad profile needs update)');
if (pub) st.pub = pub;

// ---- devices: names, presence, per-device rates ----
let leases = {};
for (let l in split(fs.readfile('/tmp/dhcp.leases') || '', '\n')) {
	let f = split(l, ' ');
	if (length(f) >= 4) leases[lc(f[1])] = { ip: f[2], name: f[3] == '*' ? null : f[3] };
}
let neigh = {};
for (let l in split(sh('ip -4 neigh'), '\n')) {
	let m = match(l, /^(\S+) dev (\S+) lladdr (\S+) (\S+)/);
	if (m) neigh[lc(m[3])] = { ip: m[1], dev: m[2], state: m[4] };
}
let assoc = {};
try { for (let r in u.call('iwinfo', 'assoclist', { device: 'phy0-ap0' }).results) assoc[lc(r.mac)] = r.signal; } catch (e) {}
let nl = {};
try {
	let j = json(sh('nlbw -c json -g mac 2>/dev/null'));
	for (let r in j.data) nl[lc(r[0])] = { rx: r[2], tx: r[4] };
} catch (e) {}

let prevNl = st.nlPrev || {}, rates = {};
for (let mac, v in nl) {
	let p = prevNl[mac];
	if (p && st.nlPrevT && now > st.nlPrevT && v.rx >= p.rx) rates[mac] = [round((v.rx - p.rx) / (now - st.nlPrevT)), round((v.tx - p.tx) / (now - st.nlPrevT))];
}
st.nlPrev = nl; st.nlPrevT = now;
wj(RUN + '/rates.json', rates);

let seen = {};
for (let mac in keys(leases)) seen[mac] = true;
for (let mac in keys(assoc)) seen[mac] = true;
for (let mac, n in neigh) if (n.dev == 'br-lan' && n.state != 'FAILED' && n.state != 'INCOMPLETE') seen[mac] = true;
for (let mac in keys(seen)) {
	let isOn = assoc[mac] != null || (neigh[mac] && index(['REACHABLE', 'DELAY', 'PROBE'], neigh[mac].state) >= 0) || (rates[mac] && rates[mac][0] + rates[mac][1] > 50);
	let d = devdb[mac];
	if (!d) {
		let nm = leases[mac] ? leases[mac].name : null;
		d = devdb[mac] = { first: now, last: isOn ? now : 0, known: false, lname: nm };
		devChanged = true;
		ev('warn', 'New device on FRWY network: ' + (nm || mac) + (leases[mac] ? ' (' + leases[mac].ip + ')' : ''));
	}
	if (isOn) { if (now - (d.last || 0) > 300) devChanged = true; d.last = now; }
	if (leases[mac] && leases[mac].name && d.lname != leases[mac].name) { d.lname = leases[mac].name; devChanged = true; }
}

// ---- top processes (busybox top, 2nd sample is the real one) ----
let tp = split(sh('top -b -n 2 -d 1 2>/dev/null'), '\n'), sec = 0, procs = [];
for (let l in tp) {
	if (match(l, /^\s*PID\s+PPID/)) { sec++; continue; }
	if (sec < 2) continue;
	let m = match(l, /^\s*(\d+)\s+\d+\s+(\S+)\s+\S+\s+(\S+)\s+\S+%\s+(\d+)%\s+(.*)$/);
	if (m && length(procs) < 10) push(procs, { pid: +m[1], user: m[2], vsz: m[3], cpu: +m[4], cmd: substr(m[5], 0, 70) });
}
wj(RUN + '/procs.json', procs);

// ---- persist ----
st.t = now;
wj(RUN + '/state.json', st);
if (!st.saved || now - st.saved > 3600) { st.saved = now; wj(PER + '/state.json', st); }
if (evAdded) wj(PER + '/events.json', events);
if (devChanged) wj(PER + '/devices.json', devdb);
