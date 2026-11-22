# Old iPad as the box's wall monitor

An iPad 4 (iPad3,4, A6, iOS 10.3.3 — the last version it gets) used as a
dedicated screen for the FRWY box: dashboard, root console, and a home proxy
that follows it to any network. Everything runs on the router; the iPad only
shows web pages, so any browser works the same way.

All steps run from a Linux PC over USB with libimobiledevice and
[pymobiledevice3](https://github.com/doronz88/pymobiledevice3); no Mac, no
Apple ID.

## 1. Check the iPad

```sh
sudo apt install libimobiledevice-utils usbmuxd
ideviceinfo -k ProductType; ideviceinfo -k ProductVersion; ideviceinfo -k ActivationState
```

`ideviceactivation activate` (the `ideviceactivation` package; it can be
unpacked from the .deb without root) asks Apple's server like iTunes does. If
it answers **Activation Lock**, the iPad belongs to someone else's Apple
account — only that owner (icloud.com/find → *Remove from account*) or Apple
with proof of purchase can release it. Don't bother wiping it; the lock is
server-side.

## 2. Restore (forgotten passcode, unknown state)

Put the iPad in recovery mode, then:

```sh
# official firmware, still signed for iPad3,4
curl -LO http://appldnld.apple.com/ios10.3.3/091-23117-20170719-CA973B02-6977-11E7-953B-279100BA0AE3/iPad_32bit_10.3.3_14G60_Restore.ipsw
sha1sum iPad_32bit_10.3.3_14G60_Restore.ipsw   # 88bb926d58e9e2497fcc2a941987314fd6656cc2
sudo idevicerestore --erase --no-input iPad_32bit_10.3.3_14G60_Restore.ipsw
ideviceactivation activate
```

## 3. Supervise and configure

Supervision is only possible at the *Hello* screen (after an erase). On iOS 10
`pymobiledevice3 profile erase-device` is rejected (`CommandFormatError`), so
erase on the device: *Settings → General → Reset → Erase All Content and Settings*.

```sh
pymobiledevice3 profile create-keybag ~/ipad-restore/supervision-keybag.pem home   # keep this file safe
pymobiledevice3 profile supervise --keybag ~/ipad-restore/supervision-keybag.pem home
./make-profile.py --proxy-host <public-ip-or-ddns> --proxy-pass '<squid pw>' \
    --wifi-pass '<FRWY wifi key>' -o ~/ipad-restore/ipad.mobileconfig
pymobiledevice3 profile install --keybag ~/ipad-restore/supervision-keybag.pem ~/ipad-restore/ipad.mobileconfig
```

With the keybag, later profile changes install silently over USB. Without it,
any change needs another erase.

The profile:

* **global HTTP proxy** through the box's squid (supervised-only setting),
  captive-portal logins allowed — away from home all web traffic leaves through
  the home connection;
* **FRWY Wi-Fi** auto-join (the box redirects the proxy port locally, because
  the upstream router has no NAT hairpin);
* **Router** and **Console** web clips, full-screen, placed in the dock.

## iOS 10 limits found on the way

* **No WPA3.** Joining a WPA3 or WPA2/WPA3-mixed network fails at association
  (`Failed to associate … -3900`, visible with `idevicesyslog`). The box's AP is
  therefore plain WPA2 (`psk2+ccmp`); phone hotspots must be set to WPA2.
* **Stock ttyd page stays blank** — its bundled JS needs a newer Safari. The
  console uses xterm.js 3.14.5 inlined into a custom page, the DOM renderer
  (canvas text is garbled on old Retina Safari) and a separate input line,
  because the soft keyboard does not deliver key events xterm can use.
* **No Tailscale/WireGuard apps** (need iOS 12–15 and an Apple ID) and the
  built-in IKEv2/L2TP/IPsec clients need UDP 500, which a typical upstream
  router's UPnP refuses (ports < 1024). Hence the proxy.
* The dashboard is written in ES5 with XHR and flexbox so Safari 10 runs it.
