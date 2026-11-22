#!/bin/sh
# Keep a UPnP TCP forward on the landlord router for the roaming squid proxy.
PORT=$(uci -q get squid.squid.http_port); [ -n "$PORT" ] || exit 0
IP=$(ip -4 -o addr show dev phy0-sta0 | awk "{print \$4}" | cut -d/ -f1); [ -n "$IP" ] || exit 0
OUT=$(upnpc -m phy0-sta0 -e frwy-proxy -a "$IP" "$PORT" "$PORT" TCP 86400 2>&1)
PUB=$(echo "$OUT" | sed -n "s/^ExternalIPAddress = //p")
if echo "$OUT" | grep -q "is redirected"; then
	echo "$PUB" > /tmp/public_ip; echo ok > /tmp/proxy_upnp
else
	echo fail > /tmp/proxy_upnp; logger -t proxy-upnp "mapping failed: $(echo "$OUT" | tail -1)"
fi
