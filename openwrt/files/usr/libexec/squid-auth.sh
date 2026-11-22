#!/bin/sh
# Minimal squid basic-auth helper: one "user password" line in /etc/squid/passwd.
read -r U P < /etc/squid/passwd
while read -r u p; do
	if [ -n "$U" ] && [ "$u" = "$U" ] && [ "$p" = "$P" ]; then echo OK; else sleep 1; echo ERR; fi
done
