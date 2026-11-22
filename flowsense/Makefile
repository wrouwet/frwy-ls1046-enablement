# flowsense build/deploy
GO      ?= go
ROUTER  ?= root@192.168.50.1
SSHKEY  ?=
SSH      = ssh $(if $(SSHKEY),-i $(SSHKEY))
SCP      = scp -O $(if $(SSHKEY),-i $(SSHKEY))
SEED    ?= data/seed.csv.gz

.PHONY: all test arm64 seed deploy clean

all: test arm64

test:
	$(GO) vet ./... && $(GO) test ./...

arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "-s -w" -o build/flowsense-arm64 ./cmd/flowsense

# Build the seed dataset from CESNET-TLS22/QUIC22 (see tools/export_cesnet.py)
seed:
	$(GO) run ./cmd/seedgen -in data/seed_raw.csv.gz -out $(SEED) -eval data/seed-eval.json

deploy: arm64
	$(SSH) $(ROUTER) 'mkdir -p /usr/share/flowsense /etc/flowsense; /etc/init.d/flowsense stop 2>/dev/null; true'
	$(SCP) build/flowsense-arm64 $(ROUTER):/usr/bin/flowsense
	$(SCP) packaging/openwrt/flowsense.init $(ROUTER):/etc/init.d/flowsense
	$(SSH) $(ROUTER) '[ -f /etc/config/flowsense ]' || $(SCP) packaging/openwrt/flowsense.config $(ROUTER):/etc/config/flowsense
	[ ! -f $(SEED) ] || $(SCP) $(SEED) $(ROUTER):/usr/share/flowsense/seed.csv.gz
	$(SSH) $(ROUTER) 'chmod 755 /usr/bin/flowsense /etc/init.d/flowsense; /etc/init.d/flowsense enable; /etc/init.d/flowsense start'

clean:
	rm -rf build
