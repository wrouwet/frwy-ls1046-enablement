#!/usr/bin/env bash
# Runs a command on the board through a running console.py and prints what the
# board answers. Usage: run.sh '<command>' [seconds to wait, default 2]
DIR=$(cd "$(dirname "$0")" && pwd)
LOG=$DIR/console.log
FIFO=$DIR/console.in
start=$(wc -c < "$LOG")
timeout 5 sh -c 'printf "%s\r" "$1" > "$2"' sh "$1" "$FIFO"
sleep "${2:-2}"
tail -c +$((start + 1)) "$LOG" | sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g' | tr -d '\r'
echo
