#!/bin/sh
# Configure the home-monitor stack on the FRWY box after copying openwrt/files/
# to / (and installing flowsense, see ../flowsense). Idempotent; run as root.
#
#   dashboard  uhttpd instance on :8080 serving /www-dash (iOS 10 compatible)
#   console    ttyd with a custom iOS-friendly page (/www-dash/term-inline.html)
#   watcher    /etc/init.d/dash-watch: 24 h history, events, device presence
#   proxy      squid on $PROXY_PORT with basic auth, UPnP-forwarded on the
#              upstream router by /usr/sbin/proxy-upnp.sh (for roaming clients)
set -e
PROXY_PORT=${PROXY_PORT:-38129}
LAN_IP=${LAN_IP:-192.168.50.1}

apk add squid miniupnpc conntrack >/dev/null

# --- dashboard web server (plain HTTP, separate from LuCI) ---
uci -q delete uhttpd.dash || true
uci set uhttpd.dash=uhttpd
uci add_list uhttpd.dash.listen_http='0.0.0.0:8080'
uci add_list uhttpd.dash.listen_http='[::]:8080'
uci set uhttpd.dash.home=/www-dash
uci set uhttpd.dash.cgi_prefix=/cgi-bin
uci set uhttpd.dash.redirect_https=0
uci set uhttpd.dash.max_requests=5
uci set uhttpd.dash.script_timeout=15
uci set uhttpd.dash.no_dirlists=1
uci commit uhttpd
chmod 755 /www-dash/cgi-bin/*

# --- web console: all interfaces, custom page (stock ttyd JS needs a newer browser) ---
uci -q delete ttyd.@ttyd[0].interface || true
uci -q delete ttyd.@ttyd[0].client_option || true
uci set ttyd.@ttyd[0].index=/www-dash/term-inline.html
uci commit ttyd

# --- traffic history that survives reboots ---
mkdir -p /etc/vnstat-db /etc/nlbwmon-db
sed -i 's#^;\?\s*DatabaseDir .*#DatabaseDir "/etc/vnstat-db"#' /etc/vnstat.conf
uci -q delete vnstat.@vnstat[0].interface || true
uci add_list vnstat.@vnstat[0].interface=phy0-sta0
uci add_list vnstat.@vnstat[0].interface=br-lan
uci commit vnstat
uci set nlbwmon.@nlbwmon[0].database_directory=/etc/nlbwmon-db
uci set nlbwmon.@nlbwmon[0].commit_interval=1h
uci commit nlbwmon
sysctl -p /etc/sysctl.d/90-nlbwmon.conf >/dev/null

# --- strongSwan: only the swanctl service; the legacy starter fights it ---
/etc/init.d/ipsec disable 2>/dev/null || true
/etc/init.d/ipsec stop 2>/dev/null || true
sed -i 's/^\(\s*\)load = yes/\1load = no/' /etc/strongswan.d/charon/pkcs11.conf 2>/dev/null || true

# --- roaming proxy ---
uci set squid.squid.http_port="$PROXY_PORT"
uci commit squid
if [ ! -f /etc/squid/passwd ]; then
	echo "ipad $(head -c 200 /dev/urandom | tr -dc A-Za-z0-9 | head -c 20)" > /etc/squid/passwd
	echo "proxy credentials written to /etc/squid/passwd"
fi
chown root:squid /etc/squid/passwd && chmod 640 /etc/squid/passwd
chmod 755 /usr/libexec/squid-auth.sh /usr/sbin/proxy-upnp.sh
# LAN clients reach the proxy through the public address too (no hairpin upstream)
uci -q delete firewall.proxy_reflect || true
uci set firewall.proxy_reflect=redirect
uci set firewall.proxy_reflect.name='Proxy-from-LAN'
uci set firewall.proxy_reflect.src=lan
uci set firewall.proxy_reflect.proto=tcp
uci set firewall.proxy_reflect.src_dport="$PROXY_PORT"
uci set firewall.proxy_reflect.dest_ip="$LAN_IP"
uci set firewall.proxy_reflect.dest_port="$PROXY_PORT"
uci set firewall.proxy_reflect.target=DNAT
uci commit firewall
grep -q proxy-upnp /etc/crontabs/root 2>/dev/null || echo "*/20 * * * * /usr/sbin/proxy-upnp.sh" >> /etc/crontabs/root
grep -q proxy-upnp /etc/rc.local || sed -i 's#^exit 0#(sleep 60; /usr/sbin/proxy-upnp.sh) \&\nexit 0#' /etc/rc.local

# --- flowsense: classify the proxy's CONNECT tunnels, use dashboard device names ---
if [ -f /etc/config/flowsense ]; then
	uci set flowsense.main.proxy_port="$PROXY_PORT"
	uci set flowsense.main.names_file=/etc/dash/devices.json
	uci commit flowsense
fi

chmod 755 /etc/init.d/dash-watch /usr/share/dash/*.uc
/etc/init.d/dash-watch enable
for s in uhttpd ttyd vnstat nlbwmon squid firewall cron dash-watch; do /etc/init.d/$s restart >/dev/null 2>&1 || true; done
[ -x /etc/init.d/flowsense ] && /etc/init.d/flowsense restart
/usr/sbin/proxy-upnp.sh || true
echo "dashboard: http://$LAN_IP:8080   console: http://$LAN_IP:7681   proxy: $(cat /tmp/public_ip 2>/dev/null):$PROXY_PORT"
