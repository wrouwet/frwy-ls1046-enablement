# frwy-ls1046-enablement

Bring-up notes and tools for an NXP **FRWY-LS1046A-TP** box (Layerscape LS1046A,
4× Cortex-A72 at 1.8 GHz, 4 GB RAM, Coral Edge TPU and Qualcomm QCA6174 Wi-Fi on
M.2), taken from a silent board to a multi-featured **OpenWrt 25.12** Wi-Fi router.
The long-term goal is a mesh networking node (prplMesh/EasyMesh or 802.11s).

| Component | Version |
|---|---|
| Board | FRWY-LS1046A-TP, boot switches `0010000000` (SD boot) |
| OS | OpenWrt 25.12.6, official `fsl_ls1046a-frwy-sdboot` image, Linux 6.12 |
| Bring-up check | NXP LSDK 21.08 (Ubuntu 20.04, Linux 5.10) |
| Wi-Fi | QCA6174 hw2.1, `ath10k` with firmware `SW_RM.1.1.1-00157` |
| Card | 8 GB microSD; extroot gives OpenWrt the whole card |

## Current state

- The box joins a known Wi-Fi network as its uplink and shares it through its
  own access point (`FRWY-LS1046A`, WPA2/WPA3) and all four Ethernet ports, which
  form one LAN on 192.168.50.1/24 with DHCP.
- `uplink-select` (in `openwrt/files/`) runs at boot. It chooses the best stored
  uplink and the matching channel, and recovers the radio when the uplink dies.
- Installed:
  - LuCI over HTTPS, a web terminal (ttyd) and attended sysupgrade
  - VPNs: WireGuard, OpenVPN, Tailscale, ZeroTier, strongSwan
  - Filtering: banIP, adblock, DNS-over-HTTPS
  - Traffic and monitoring: SQM, collectd statistics, nlbwmon, vnstat, UPnP, DDNS
  - Tools: tcpdump, iperf3, nmap, mtr and others
- **Filtering is deliberately off for now:** the firewall accepts everything in
  every zone (NAT stays on), and banIP, adblock, DNS-over-HTTPS and UPnP are
  disabled. Turn them on in LuCI when needed.
- `openwrt/config-snapshot/` holds the box's configuration and package list,
  with keys, passwords and the home network's name removed.

## Host tools (`host/`)

| Script | Does |
|---|---|
| `console.py` | Serial console on `/dev/ttyUSB0` (115200) for a terminal window. It also logs to `console.log` and accepts input from the `console.in` FIFO, so a script can drive the same console. It waits for the port while the board is off. Ctrl-] quits. |
| `run.sh '<cmd>'` | Sends a command through `console.py` and prints the answer. |
| `backup-sd.sh <dev> <name>` | Images a whole card to `.img.zst`, takes a SHA-256 and checks it. |
| `install-lsdk.sh <dev>` | Writes NXP LSDK 21.08 for the FRWY with NXP's `flex-installer` (needs sudo). Expects the downloads in `downloads/lsdk2108/`. |
| `write-openwrt.sh <dev> [ver]` | Downloads, verifies, writes and reads back the OpenWrt FRWY SD image. |

The console is a Silicon Labs CP2102N on the board side, powered by the board:
`/dev/ttyUSB0` only exists while the board has power. To use it without sudo:

```bash
echo 'SUBSYSTEMS=="usb", ATTRS{idVendor}=="10c4", TAG+="uaccess"' | \
    sudo tee /etc/udev/rules.d/70-silabs-uart.rules
sudo udevadm control --reload-rules && sudo udevadm trigger --subsystem-match=tty
```

## Rebuilding the box

1. **Card:** `host/write-openwrt.sh /dev/sdX`, then boot the board from it.
2. **Console:** run `host/console.py` in a terminal and press Enter. Root has no
   password on a fresh image, so set one with `passwd`.
3. **LAN off the home subnet:** the default 192.168.1.1 with a DHCP server
   collides with most home networks.
   ```sh
   uci set network.lan.ipaddr='192.168.50.1/24'; uci commit network; /etc/init.d/network reload
   ```
4. **Internet for the first installs:** cable the WAN port to a Linux laptop that
   shares its connection, for example with NetworkManager:
   ```bash
   nmcli con add type ethernet ifname <eth> con-name frwy-share ipv4.method shared \
       ipv4.addresses 10.42.0.1/24 ipv6.method disabled connection.autoconnect no
   nmcli con up frwy-share
   ```
5. **Wi-Fi driver:**
   ```sh
   apk update
   apk add kmod-ath10k ath10k-firmware-qca6174 wpad-mbedtls iw
   reboot   # netifd and wpad only pick up Wi-Fi support after a restart
   ```
6. **Uplinks and access point:** add one `mode sta` wifi-iface per known network
   (`network wwan`, `encryption psk2`, `key ...`) and put `wwan` in the WAN zone.
   Add an AP wifi-iface on `network lan`. See `openwrt/config-snapshot/wireless`.
7. **uplink-select:** copy `openwrt/files/` to `/`, then
   `/etc/init.d/uplink-select enable`, and list both files in
   `/etc/sysupgrade.conf`.
8. **Extroot:** the image leaves only about 100 MB writable. Add a third partition
   over the rest of the card and move `/overlay` there:
   ```sh
   apk add block-mount e2fsprogs kmod-fs-ext4 sfdisk partx-utils
   echo "346112,,83" | sfdisk --append --no-reread /dev/mmcblk0; partx -a /dev/mmcblk0
   mkfs.ext4 -L extroot /dev/mmcblk0p3
   # then the standard steps from https://openwrt.org/docs/guide-user/additional-software/extroot_configuration
   ```
9. **Packages:** `apk add $(cat openwrt/config-snapshot/installed-packages.txt)`,
   or pick from it.

## What uplink-select does

Every `mode sta` wifi-iface in `/etc/config/wireless` is a known uplink.

- **At boot:** it scans and takes the strongest known network, preferring 5 GHz
  unless it is more than 6 dB weaker. It skips the 5 GHz DFS channels (52-144) and
  pins the client to the chosen access point's BSSID. It moves the radio, and with
  it the AP, to that channel at 20 MHz, and sets the country code from the access
  point's beacon. It only reloads Wi-Fi when something actually changed.
- **While running:** it checks internet every minute. After 3 failed minutes it
  picks again, and if the uplink is still down it reloads the `ath10k` driver.

Logs: `logread -e uplink-select`.

## Hardware findings

- **QCA6174 interface combinations:** managed ≤ 2, AP/mesh point ≤ 2, total ≤ 4,
  all on **one channel**. The box can be a client upstream and an AP or 802.11s
  mesh point at the same time, but every interface shares one radio and channel.
- **No AP on DFS channels:** when the uplink was on 5 GHz channel 100, hostapd
  logged `DFS CAC required on new channel` and then `Unable to setup interface`.
  The AP must follow the uplink's channel, so the uplink has to stay off DFS
  channels. That is why uplink-select skips them.
- **Firmware crashes:** the only firmware for this hw2.1 chip crashed
  (`ath10k ... firmware crashed!`) when interfaces were reconfigured, and the
  driver did not always recover. Hence uplink-select's minimal reloads and its
  driver reload. A newer AP-class M.2 card would be the robust fix for a mesh
  node.
- **Weak antennas:** with the antennas fitted at the time, a phone associated and
  was then dropped for `excessive missing ACKs`.

## Pitfalls hit along the way

- **A failing SD card makes a silent board.** With the boot switches on SD, the
  original card was bad enough that nothing printed at all, not even the first
  boot stage. When written to, it reported SCSI `Hardware Error` and Linux
  remounted the filesystem read-only. A new card booted at once.
- **Don't power off the card with udisks.** On this laptop
  `udisksctl power-off` removed the whole internal USB card reader, and only root
  could bring it back (`echo 0/1 > /sys/bus/usb/devices/usb2/authorized`).
  Unmount only.
- **Two programs on one serial port each see half the bytes.** A detached
  `screen` session kept stealing console output until it was killed.
- **https-dns-proxy broke all DNS.** Once installed, it points dnsmasq only at
  DNS-over-HTTPS. On a network that blocks DoH, every lookup then fails, and
  `wget` reports the misleading `Failed to send request: Operation not
  permitted`. It stays disabled.
- **NetworkManager link-local profiles** grab the laptop's Ethernet whenever the
  link drops, so a DHCP client profile has to be brought up again after the box
  reboots.

## License

Apache-2.0
