#!/usr/bin/env bash
# Images a whole SD card to <name>.img.zst with a SHA-256 of the raw data and a
# copy of its partition table, then checks the image decompresses to that hash.
# Usage: backup-sd.sh <device> <name>   e.g. backup-sd.sh /dev/sda frwy-sd-backup
# Needs read access to the device (sudo setfacl -m u:$USER:rw /dev/sdX).
set -euo pipefail
DEV=$1 NAME=$2
dd if="$DEV" bs=4M iflag=fullblock status=progress \
    | tee >(sha256sum | sed "s|-|$NAME.img|" > "$NAME.img.sha256") \
    | zstd -T0 -3 -q -o "$NAME.img.zst"
sleep 2
sfdisk -d "$DEV" > "$NAME.partitions.txt" || true
zstd -dc "$NAME.img.zst" | sha256sum | sed "s|-|$NAME.img|" | diff - "$NAME.img.sha256"
echo "Backup verified: $NAME.img.zst"
