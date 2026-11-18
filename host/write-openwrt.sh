#!/usr/bin/env bash
# Downloads the official OpenWrt SD card image for the FRWY-LS1046A, checks it
# against OpenWrt's sha256sums, writes it to the card and compares it back.
# Usage: write-openwrt.sh <device> [version, default 25.12.6]
# Needs write access to the device (sudo setfacl -m u:$USER:rw /dev/sdX).
set -euo pipefail
DEV=$1
VER=${2:-25.12.6}
DIR=$(cd "$(dirname "$0")/.." && pwd)/downloads/openwrt-$VER
URL=https://downloads.openwrt.org/releases/$VER/targets/layerscape/armv8_64b
IMG=openwrt-$VER-layerscape-armv8_64b-fsl_ls1046a-frwy-sdboot-squashfs-sdcard.img.gz

name=$(basename "$DEV")
if [ "$(cat /sys/block/$name/removable)" != 1 ]; then
    echo "$DEV is not removable; refusing to write to it." >&2
    exit 1
fi
if grep -q "^$DEV" /proc/mounts; then
    echo "$DEV has mounted partitions; unmount them first." >&2
    exit 1
fi

mkdir -p "$DIR" && cd "$DIR"
[ -f "$IMG" ] || curl -fLO "$URL/$IMG"
curl -fsSLO "$URL/sha256sums"
grep " \*\?$IMG\$" sha256sums | sha256sum -c -

size=$(zcat "$IMG" | wc -c)
zcat "$IMG" | dd of="$DEV" bs=4M oflag=direct conv=fsync status=progress
# The write bypassed the page cache, so this read comes from the card.
cmp <(head -c "$size" "$DEV") <(zcat "$IMG")
echo "OpenWrt $VER written to $DEV and verified."
