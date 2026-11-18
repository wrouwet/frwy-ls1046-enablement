#!/usr/bin/env bash
# Writes NXP LSDK 21.08 for the FRWY-LS1046A to the SD card in the laptop's
# reader with NXP's flex-installer: composite firmware at 4 KiB, then a misc,
# boot, backup and system (Ubuntu 20.04) partition. Asks for sudo once.
#
# Refuses to run unless the target is a removable card of 7 to 64 GiB.
set -euo pipefail

DEV=${1:-/dev/sda}
DIR=${LSDK_DIR:-$(cd "$(dirname "$0")/.." && pwd)/downloads/lsdk2108}
NAME=$(basename "$DEV")

size_gib=$(( $(cat /sys/block/$NAME/size) * 512 / 1024 / 1024 / 1024 ))
removable=$(cat /sys/block/$NAME/removable)
if [ "$size_gib" -lt 7 ] || [ "$size_gib" -gt 64 ] || [ "$removable" != 1 ]; then
    echo "$DEV is ${size_gib} GiB, removable=$removable: not the SD card, stopping." >&2
    exit 1
fi
# A previous flex-installer run leaves its partitions mounted under /mnt;
# drop those, then make the kernel read the card's own partition table.
for m in $(awk -v d="^$DEV" '$1 ~ d && $2 ~ "^/mnt/" {print $2}' /proc/mounts); do
    sudo umount -l "$m"
done
if grep -q "^$DEV" /proc/mounts; then
    echo "$DEV has partitions mounted elsewhere; unmount them first." >&2
    exit 1
fi
sudo blockdev --rereadpt "$DEV" || true

lsblk -o NAME,SIZE,MODEL "$DEV"
echo
echo "Installing LSDK 21.08 (ls1046afrwy) to $DEV. Everything on it is replaced."
cd "$DIR"
chmod +x flex-installer
yes y | sudo ./flex-installer \
    -m ls1046afrwy \
    -f firmware_ls1046afrwy_sdboot.img \
    -b boot_LS_arm64_lts_5.10.tgz \
    -r rootfs_lsdk2108_ubuntu_main_arm64.tgz \
    -d "$DEV"
sync
# Let the logged-in user read the card back to check the result.
sudo setfacl -m "u:$(id -un):rw" "$DEV"
echo
echo "INSTALL-DONE"
