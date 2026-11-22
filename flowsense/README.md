# flowsense

Encrypted-traffic **application classifier for OpenWrt routers**. It watches the
LAN bridge and tells you what each device is doing — *Gaming (Roblox)*,
*Video streaming (YouTube)*, *Chat & calls (Discord)*, *Web browsing* — using
only metadata: packet sizes, directions, timing and a few names that flows still
leak in clear text. A random-forest model is **trained and retrained on the
router itself** (developed on an NXP LS1046A, 4× Cortex-A72, OpenWrt 25.12).

Single static Go binary, no dependencies on the router, no libpcap.

## How it works

```
br-lan ──AF_PACKET──► flow table ──► names?  (TLS SNI · QUIC SNI · DNS · HTTP Host · proxy CONNECT)
                         │              │ yes → app (rule table) ──► label for training
                         │              ▼ no
                         │          operator address ranges / game ports (Roblox, Riot, Valve, Xbox)
                         │              ▼ no
                         │          realtime-UDP behaviour (game / voice pattern)
                         │              ▼ no
                         └─features─► random forest (≥ min confidence) ──► app, else "Unknown"
```

1. **Capture** – raw `AF_PACKET` socket on the LAN bridge; IPv4 TCP/UDP headers
   decoded in user space. GRO super-packets are split into estimated wire packets.
2. **Flow features (71)** – modelled on the *PPI* representation of the CESNET
   datasets and computed **only from the packets seen so far**: signed payload
   sizes of the first 30 data packets, PPI duration and round-trips,
   per-direction size statistics, inter-packet-time statistics, an 8-bin size
   histogram, the first five sizes in each direction, burst statistics and
   direction ratios (`internal/flow`). Flows are classified at 8, 15 and 30
   packets, and training samples are taken at exactly the same points (the seed
   data is augmented with the same prefixes), so the model never sees a
   different kind of input live than in training — a test enforces this.
3. **Self-labelling** – most flows still reveal a name: the TLS ClientHello SNI,
   the QUIC Initial SNI (decrypted with the public RFC 9001 initial keys and
   reassembled across packets), the DNS answer that resolved the server, an HTTP
   Host header or a proxy CONNECT target. Only the certain ones (SNI, Host,
   CONNECT) become training labels; DNS-derived names on shared CDN addresses
   are used for display but not for training (`internal/label`).
4. **Two models, trained on the router**
   * a dependency-free **random forest** (CART, Gini, bootstrap, random feature
     subspaces, quantile split candidates), retrained every 15 min once 100 new
     local samples exist;
   * a small **neural network** (MLP 71→64→32→classes, Adam) that learns
     **continuously**: every minute it takes gradient steps on the newly
     labelled flows mixed with class-balanced replay from the sample pool, so it
     adapts to the home network without forgetting;
   * an **ensemble** whose mixing weight is picked on the hold-out split at each
     retrain and then shifted by each model's live agreement.
   Local samples are weighted 3× and evaluated separately ("your test"), and
   the learning curve is kept in `/etc/flowsense/history.json` (`internal/ml`).
5. **API** – `127.0.0.1:8091/api/{now,today,flows,model,all}`; JSON with the
   top running apps, per-device usage, 24 h history and model statistics. The
   API deliberately exposes activities and apps, not domains or addresses.

## Seed model (public data)

The seed set is built from the **CESNET-TLS22** and **CESNET-QUIC22** datasets
(XS size) via CESNET DataZoo; the CESNET service labels are mapped onto
flowsense apps and capped per class (`tools/export_cesnet.py`, `cmd/seedgen`,
which computes features with the same Go code the router runs).

Hold-out result on the seed data (80/20 split, 139 826 prefix samples from
150 962 flows, 29 classes; each flow judged at 8, 15 and 30 packets):

| model | accuracy |
|---|---|
| random forest (60 trees) | **0.928** |
| neural network | 0.804 |
| ensemble (weight picked on hold-out) | 0.928 (network weight 0 on public data) |

Per app (forest F1): Instagram 0.99, X 0.98, YouTube 0.97–0.98, Xbox Live 0.98,
Spotify 0.97, Discord 0.93–0.94, Valorant/Riot 0.95, Steam 0.87, adult sites
0.83–0.84, generic web browsing 0.74–0.75. On public data the network adds
nothing over the forest; its job is to adapt online to the home network, and
the ensemble weight moves towards it only if its live agreement is better.

The most important features are the sizes of the first data packets
(ClientHello and first server records) — consistent with early-classification
literature. **Caveat:** the split is from the same network and week; accuracy on
a different network and later time is lower (the papers below document this
drift), which is why flowsense keeps learning from locally labelled flows and
shows its live agreement rather than only the offline number. The first
deployment (complete-flow features, no local data) agreed with visible names on
only 18 % of live flows — mostly because Roblox and Fortnite were missing from
the public data and early-flow features differed from training; this version
fixes both causes.

Training on the LS1046A: ~2 minutes for ~100k samples; resident memory
≈ 150 MB.

## Build & deploy

```sh
make test arm64                                     # go 1.23+, static arm64 binary
python tools/export_cesnet.py 4000                  # needs cesnet-datazoo (Python 3.10–3.12)
make seed                                           # data/seed.csv.gz
make deploy ROUTER=root@192.168.50.1 SSHKEY=~/.ssh/id_ed25519   # from this flowsense/ directory
```

Config: `/etc/config/flowsense` (interface, client network, router address,
local proxy port to classify CONNECT tunnels, device-names file, confidence
threshold). Data: `/etc/flowsense` (model, evaluation, local samples, counters).

## Limits

* Only IPv4; traffic that never leaves the LAN is ignored.
* Names are absent for Encrypted Client Hello, DoH-resolved bare IPs and most
  game UDP — that is exactly where the model and the address/behaviour rules
  are used; such predictions are labelled as such in the UI.
* Address ranges for game operators drift; they are a hint, not ground truth.
* Use on networks you own or administer.

## References

* V. Draper-Gil, A. H. Lashkari, M. S. I. Mamun, A. A. Ghorbani, *Characterization of Encrypted and VPN Traffic using Time-related Features*, ICISSP 2016. doi:10.5220/0005740704070414 (ISCX VPN-nonVPN dataset)
* L. Bernaille, R. Teixeira, I. Akodjenou, A. Soule, K. Salamatian, *Traffic Classification on the Fly*, ACM SIGCOMM CCR 36(2), 2006 — first-packets classification.
* J. Luxemburk, T. Čejka, *Fine-grained TLS services classification with reject option*, Computer Networks 2023 (CESNET-TLS22). arXiv:2202.11984
* J. Luxemburk, K. Hynek, T. Čejka et al., *CESNET-QUIC22: a large one-month QUIC network traffic dataset from backbone lines*, Data in Brief 2023. Zenodo 10.5281/zenodo.7409923
* J. Luxemburk, K. Hynek, *DataZoo: Streamlining Traffic Classification Experiments*, 2023. arXiv:2310.19568 — https://github.com/CESNET/cesnet-datazoo
* S. Rezaei, X. Liu, *Deep Learning for Encrypted Traffic Classification: An Overview*, IEEE Communications Magazine 2019. arXiv:1810.07906
* A. Alwhbi, C. Zou, R. Alharbi, *Encrypted Network Traffic Analysis and Classification Utilizing Machine Learning*, Sensors 24(11), 2024.
* M. Thomson, S. Turner (eds.), *RFC 9001: Using TLS to Secure QUIC* — Initial packet protection (test vectors used in `internal/label`).

## License

Apache-2.0 (see the repository root).
