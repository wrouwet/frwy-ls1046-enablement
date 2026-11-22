"""Export raw PPI + flow counters from CESNET-TLS22 / CESNET-QUIC22 (XS) for the
flowsense seed model. Features are computed later by flowsense's own Go code.
Output CSV: app,dataset,udp,bytes,bytes_rev,packets,packets_rev,s1..s30,t1..t30
(s = signed payload size, + client->server; t = inter-packet time ms)."""
import csv, gzip, sys, collections
import numpy as np
from cesnet_datazoo.datasets import CESNET_TLS22, CESNET_QUIC22
from cesnet_datazoo.config import DatasetConfig, AppSelection

MAP = {
 'youtube': 'YouTube', 'netflix': 'Netflix', 'twitch': 'Twitch', 'amazon-prime': 'Prime Video', 'vimeo': 'Vimeo', 'o2tv': 'Web browsing',
 'discord': 'Discord', 'spotify': 'Spotify', 'soundcloud': 'SoundCloud', 'shazam': 'Web browsing',
 'whatsapp': 'WhatsApp', 'signal-cdn': 'WhatsApp',
 'zoom': 'Zoom / Teams', 'teams': 'Zoom / Teams', 'skype': 'Zoom / Teams', 'webex': 'Zoom / Teams', 'google-hangouts': 'Zoom / Teams', 'slack': 'Zoom / Teams',
 'xbox-live': 'Xbox Live', 'riot-games': 'Valorant / Riot', 'steam': 'Steam', 'csgo-market': 'Web browsing', 'ea-games': 'EA',
 'king-games': 'Mobile games', 'unity-games': 'Mobile games', 'unitygames': 'Mobile games', 'gamedock': 'Mobile games', 'easybrain': 'Mobile games',
 'blitz-gg': 'Web browsing',
 'bongacams': 'Adult sites', 'spanbang': 'Adult sites', 'xhamster': 'Adult sites',
 'tiktok': 'TikTok', 'instagram': 'Instagram', 'snapchat': 'Snapchat', 'twitter': 'X / Twitter', 'tinder': 'Social (other)', 'vkontakte': 'Social (other)',
 'facebook-graph': 'Facebook', 'facebook-media': 'Facebook', 'facebook-messenger': 'Facebook', 'facebook-web': 'Facebook', 'facebook-connect': 'Facebook',
 'facebook-gamesgraph': 'Facebook', 'facebook-rupload': 'Facebook',
 'microsoft-update': 'Windows / Xbox updates', 'vscode-update': 'Windows / Xbox updates', 'opera-autoupdate': 'Windows / Xbox updates',
 'apple-icloud': 'Apple / iCloud', 'apple-itunes': 'Apple / iCloud', 'apple-updates': 'Apple / iCloud', 'apple-push': 'Apple / iCloud',
 'dropbox': 'Cloud storage', 'microsoft-onedrive': 'Cloud storage', 'google-drive': 'Cloud storage', 'owncloud': 'Cloud storage', 'google-photos': 'Cloud storage',
 'github': 'AI & dev', 'gitlab': 'AI & dev', 'npm-registry': 'AI & dev', 'docker-registry': 'AI & dev', 'kaggle': 'AI & dev', 'google-colab': 'AI & dev',
 'google-ads': 'Ads & tracking', 'adobe-ads': 'Ads & tracking', 'appnexus': 'Ads & tracking', 'pubmatic': 'Ads & tracking', 'rubiconproject': 'Ads & tracking',
 'amazon-advertising': 'Ads & tracking', 'openx': 'Ads & tracking', 'connectad': 'Ads & tracking', 'google-imasdk': 'Ads & tracking',
 'dns-doh': 'DNS',
}
SKIP = {'default', 'default-background', 'google-background', 'facebook-background'}
CAP = int(sys.argv[1]) if len(sys.argv) > 1 else 4000

out = gzip.open('data/seed_raw.csv.gz', 'wt', newline='')
w = csv.writer(out)
w.writerow(['app', 'dataset', 'udp', 'bytes', 'bytes_rev', 'packets', 'packets_rev'] + [f's{i}' for i in range(1, 31)] + [f't{i}' for i in range(1, 31)])
counts = collections.Counter()
for cls, udp in ((CESNET_TLS22, 0), (CESNET_QUIC22, 1)):
    ds = cls(data_root='data/' + cls.__name__, size='XS')
    period = list(ds.time_periods.keys())[0]
    cfg = DatasetConfig(dataset=ds, apps_selection=AppSelection.ALL_KNOWN, train_period_name=period, train_size=1500000)
    ds.set_dataset_config_and_initialize(cfg)
    enum = ds._tables_app_enum
    df = ds.get_train_df(flatten_ppi=False)
    print(cls.__name__, period, df.shape, flush=True)
    per = collections.Counter()
    for ppi, b, br, p, pr, app in zip(df['PPI'], df['BYTES'], df['BYTES_REV'], df['PACKETS'], df['PACKETS_REV'], df['APP']):
        name = enum[int(app)]
        if name in SKIP:
            continue
        lab = MAP.get(name, 'Web browsing')
        cap = CAP * 3 if lab == 'Web browsing' else CAP
        if per[lab] >= cap:
            continue
        per[lab] += 1
        ipt, d, size = ppi[0], ppi[1], ppi[2]
        n = int((size > 0).sum()) if (size > 0).any() else 0
        s = [int(size[i] * (1 if d[i] > 0 else -1)) if i < n else 0 for i in range(30)]
        t = [int(ipt[i]) if i < n else 0 for i in range(30)]
        w.writerow([lab, cls.__name__, udp, int(b), int(br), int(p), int(pr)] + s + t)
    counts.update(per)
    print(' ', dict(per.most_common()), flush=True)
out.close()
print('TOTAL', sum(counts.values()), dict(counts.most_common()))
