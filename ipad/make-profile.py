#!/usr/bin/env python3
"""Build the iPad configuration profile (.mobileconfig) for the FRWY home monitor.

Contains: a global HTTP proxy through the box (needs a *supervised* iPad), the
box's Wi-Fi, web clips for the dashboard and console, and a home-screen layout
that puts them in the dock. Secrets are passed on the command line and only
end up in the output file -- keep that file out of git.

  ./make-profile.py --proxy-host 203.0.113.7 --proxy-pass '...' \
      --wifi-pass '...' -o ~/ipad-restore/ipad.mobileconfig
"""
import argparse, plistlib, uuid


def payload(ptype, ident, name, **kw):
    d = {'PayloadType': ptype, 'PayloadVersion': 1, 'PayloadIdentifier': 'home.frwy.' + ident,
         'PayloadUUID': str(uuid.uuid4()).upper(), 'PayloadDisplayName': name}
    d.update(kw)
    return d


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--org', default='home')
    ap.add_argument('--router', default='192.168.50.1')
    ap.add_argument('--proxy-host', help='public address of the box (omit to skip the proxy)')
    ap.add_argument('--proxy-port', type=int, default=38129)
    ap.add_argument('--proxy-user', default='ipad')
    ap.add_argument('--proxy-pass')
    ap.add_argument('--wifi-ssid', default='FRWY-LS1046A')
    ap.add_argument('--wifi-pass')
    ap.add_argument('-o', '--out', required=True)
    a = ap.parse_args()

    dash, term = f'http://{a.router}:8080/', f'http://{a.router}:7681/'
    p = []
    if a.proxy_host:
        p.append(payload('com.apple.proxy.http.global', 'proxy', 'Home proxy', ProxyType='Manual',
                         ProxyServer=a.proxy_host, ProxyServerPort=a.proxy_port,
                         ProxyUsername=a.proxy_user, ProxyPassword=a.proxy_pass or '', ProxyCaptiveLoginAllowed=True))
    if a.wifi_pass:
        p.append(payload('com.apple.wifi.managed', 'wifi', a.wifi_ssid, SSID_STR=a.wifi_ssid, EncryptionType='WPA',
                         Password=a.wifi_pass, AutoJoin=True, HIDDEN_NETWORK=False))
    p.append(payload('com.apple.webClip.managed', 'clip.dash', 'Router', URL=dash, Label='Router', FullScreen=True, IsRemovable=True, Precomposed=True))
    p.append(payload('com.apple.webClip.managed', 'clip.term', 'Console', URL=term, Label='Console', FullScreen=True, IsRemovable=True, Precomposed=True))
    W = lambda u, n: {'Type': 'WebClip', 'URL': u, 'DisplayName': n}
    A = lambda b: {'Type': 'Application', 'BundleID': b}
    p.append(payload('com.apple.homescreenlayout', 'layout', 'Home screen layout',
                     Dock=[W(dash, 'Router'), W(term, 'Console'), A('com.apple.mobilesafari'), A('com.apple.Preferences')],
                     Pages=[[W(dash, 'Router'), W(term, 'Console'), A('com.apple.mobilesafari'), A('com.apple.Preferences')]]))
    prof = {'PayloadType': 'Configuration', 'PayloadVersion': 1, 'PayloadIdentifier': 'home.frwy.ipad',
            'PayloadUUID': str(uuid.uuid4()).upper(), 'PayloadDisplayName': 'FRWY home monitor',
            'PayloadOrganization': a.org, 'PayloadRemovalDisallowed': False, 'PayloadContent': p}
    with open(a.out, 'wb') as f:
        plistlib.dump(prof, f)
    print('wrote', a.out, [x['PayloadType'] for x in p])


if __name__ == '__main__':
    main()
