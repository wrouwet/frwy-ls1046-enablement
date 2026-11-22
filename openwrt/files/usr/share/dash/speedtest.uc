#!/usr/bin/ucode
// Speed test against Cloudflare; appends the result to /etc/dash/speedtests.json.
'use strict';
import * as fs from 'fs';
function round(x) { return x == null ? null : int(x < 0 ? x - 0.5 : x + 0.5); }   // ucode has no round()

const LOCK = '/tmp/dash/speedtest.running', OUT = '/etc/dash/speedtests.json';
if (fs.stat(LOCK) && time() - fs.stat(LOCK).mtime < 120) exit(0);
fs.writefile(LOCK, 'down');

function sh(c) { let p = fs.popen(c); if (!p) return ''; let s = p.read('all'); p.close(); return s || ''; }
function num(s) { let m = match(s || '', /([0-9.]+)/); return m ? +m[1] : null; }

try {
let pg = match(sh('ping -c 5 -W 1 -q speed.cloudflare.com 2>/dev/null'), /= [0-9.]+\/([0-9.]+)\//);
let down = num(sh("curl -s -o /dev/null -m 25 -w '%{speed_download}' 'https://speed.cloudflare.com/__down?bytes=25000000'"));
fs.writefile(LOCK, 'up');
let up = num(sh("head -c 8000000 /dev/zero | curl -s -o /dev/null -m 25 -w '%{speed_upload}' --data-binary @- 'https://speed.cloudflare.com/__up'"));

let res = { t: time(), down: down != null ? round(down * 8 / 10000.0) / 100.0 : null, up: up != null ? round(up * 8 / 10000.0) / 100.0 : null, ping: pg ? +pg[1] : null };
let s = fs.readfile(OUT), arr = [];
if (s) { try { arr = json(s); } catch (e) {} }
push(arr, res);
if (length(arr) > 100) arr = slice(arr, length(arr) - 100);
fs.writefile(OUT + '.tmp', sprintf('%J', arr)); fs.rename(OUT + '.tmp', OUT);
} catch (e) {
	system(sprintf('logger -t speedtest %s', sprintf('%J', '' + e)));
}
fs.unlink(LOCK);
