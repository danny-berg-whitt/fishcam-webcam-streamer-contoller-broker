# Build the FishCam images with podman and publish them to GitHub Container
# Registry (ghcr.io). Nothing here uses Docker's tools or services.
#
#   make login       # once per machine: podman login ghcr.io
#   make release     # build all three images for the Pi and push them
#
# Run `make help` for every target and the current settings.

REGISTRY  ?= ghcr.io/danny-berg-whitt
TAG       ?= latest
PLATFORM  ?= linux/arm64
PODMAN    ?= podman
GHCR_USER ?= $(notdir $(REGISTRY))

# kubectl on PATH (a workstation whose kubeconfig points at the cluster),
# else MicroK8s's bundled one when run on the Pi. Override with KUBECTL=...
ifeq ($(origin KUBECTL),undefined)
KUBECTL := $(shell if command -v kubectl >/dev/null 2>&1; then echo kubectl; \
             elif command -v microk8s >/dev/null 2>&1; then echo microk8s kubectl; \
             else echo kubectl; fi)
endif

# Must match the nodeSelector in k8s/deployment.yaml.
WEBCAM_LABEL_KEY   := fishcam.berg-whitt.com/webcam
WEBCAM_LABEL_VALUE := c922

COMPONENTS := streamer controller broker
ARCH       := $(lastword $(subst /, ,$(PLATFORM)))
image       = $(REGISTRY)/fishcam-$(1):$(TAG)

.DEFAULT_GOAL := help
.PHONY: help login rootfs build push release test clean ghcr-pull-secret \
        secret label-node deploy status logs ingress

help:
	@echo "Targets:"
	@echo "  login             podman login ghcr.io as GHCR_USER (token from GHCR_TOKEN, else prompted)"
	@echo "  rootfs            fetch and verify Alpine's root filesystem for the streamer"
	@echo "  build             build all three images for PLATFORM"
	@echo "  push              push all three images to REGISTRY"
	@echo "  release           build, then push"
	@echo "  test              go vet and go test for each component"
	@echo "  clean             remove locally built images and the fetched rootfs"
	@echo
	@echo "  secret            create the webcam-hmac secret (once; never overwrites)"
	@echo "  broker-init       create webcam-broker-tokens with one user (NAME=admin)"
	@echo "  broker-add-user NAME=...  /  broker-list-users"
	@echo "  ghcr-pull-secret  create the ghcr-pull secret (only for private packages)"
	@echo "  label-node        label the node with the webcam (done by deploy)"
	@echo "  deploy            label-node, check secrets, apply configmap/deployment/service"
	@echo "  status            deployment, pods and service"
	@echo "  logs              recent container logs (C=streamer|controller|broker, default streamer)"
	@echo "  ingress           publish the broker at /webcam/* (separate from deploy on purpose)"
	@echo
	@echo "Settings:"
	@echo "  REGISTRY=$(REGISTRY)"
	@echo "  TAG=$(TAG)"
	@echo "  PLATFORM=$(PLATFORM)"
	@echo "  GHCR_USER=$(GHCR_USER)"
	@echo "  KUBECTL=$(KUBECTL)"
	@echo "  images:"
	@$(foreach c,$(COMPONENTS),echo "    $(call image,$(c))";)

# A personal access token (classic) with the write:packages scope. With
# GHCR_TOKEN unset, podman prompts for it instead.
login:
	@if [ -n "$$GHCR_TOKEN" ]; then \
	  printf '%s' "$$GHCR_TOKEN" | $(PODMAN) login ghcr.io -u "$(GHCR_USER)" --password-stdin; \
	else \
	  $(PODMAN) login ghcr.io -u "$(GHCR_USER)"; \
	fi

rootfs:
	streamer/fetch-rootfs.sh $(ARCH)

# The streamer's runtime stage runs `apk add` for the target architecture,
# so build it natively (arm64 on an Apple Silicon Mac or on the Pi) or with
# emulation. The controller and broker cross-compile and need neither.
build: rootfs
	@set -e; for c in $(COMPONENTS); do \
	  echo "==> $$c"; \
	  $(PODMAN) build --platform $(PLATFORM) --tag $(REGISTRY)/fishcam-$$c:$(TAG) ./$$c; \
	done

push:
	@set -e; for c in $(COMPONENTS); do \
	  $(PODMAN) push $(REGISTRY)/fishcam-$$c:$(TAG); \
	done

release: build push

test:
	@set -e; for c in $(COMPONENTS); do \
	  echo "==> $$c"; \
	  (cd $$c && go vet ./... && go test -count=1 ./...); \
	done

clean:
	-@for c in $(COMPONENTS); do $(PODMAN) rmi $(REGISTRY)/fishcam-$$c:$(TAG) 2>/dev/null; done
	rm -rf streamer/rootfs

# GHCR packages start out private. Either make them public in each package's
# settings on GitHub, or give the cluster read access with this secret,
# which k8s/deployment.yaml references as imagePullSecrets. The token needs
# only the read:packages scope. (`docker-registry` is kubectl's name for any
# registry-login secret; no Docker software is involved.)
ghcr-pull-secret:
	@test -n "$$GHCR_TOKEN" || { echo "usage: GHCR_TOKEN=<token> make ghcr-pull-secret"; exit 1; }
	$(KUBECTL) create secret docker-registry ghcr-pull \
	  --docker-server=ghcr.io \
	  --docker-username="$(GHCR_USER)" \
	  --docker-password="$$GHCR_TOKEN"

# ---------------------------------------------------------------------------
# Cluster
# ---------------------------------------------------------------------------

# Refuses to replace an existing secret: rotating HMAC_SECRET is a deliberate
# act (delete the secret, run this, then restart the deployment).
secret:
	@if $(KUBECTL) get secret webcam-hmac >/dev/null 2>&1; then \
	  echo "webcam-hmac already exists; left unchanged."; \
	  echo "To rotate: $(KUBECTL) delete secret webcam-hmac && make secret && $(KUBECTL) rollout restart deployment/webcam"; \
	else \
	  $(KUBECTL) create secret generic webcam-hmac \
	    --from-literal=HMAC_SECRET="$$(openssl rand -hex 32)" && \
	  echo "created webcam-hmac"; \
	fi

# Labels the node the pod's nodeSelector looks for. Already labelled is a
# no-op; with one node it labels that one; with several it stops, since only
# you know which has the camera plugged in.
label-node:
	@labelled=$$($(KUBECTL) get nodes -l $(WEBCAM_LABEL_KEY)=$(WEBCAM_LABEL_VALUE) -o name); \
	if [ -n "$$labelled" ]; then \
	  echo "already labelled: $$labelled"; exit 0; \
	fi; \
	nodes=$$($(KUBECTL) get nodes -o name); \
	count=$$(printf '%s\n' "$$nodes" | grep -c . || true); \
	if [ "$$count" -eq 1 ]; then \
	  $(KUBECTL) label $$nodes $(WEBCAM_LABEL_KEY)=$(WEBCAM_LABEL_VALUE); \
	else \
	  echo "$$count nodes and none labelled; label the one with the webcam:"; \
	  printf '  %s\n' $$nodes; \
	  echo "  $(KUBECTL) label node <node> $(WEBCAM_LABEL_KEY)=$(WEBCAM_LABEL_VALUE)"; \
	  exit 1; \
	fi

# Checks the two secrets the pod can't start without, so a missing one fails
# here with a pointer instead of as CreateContainerConfigError in the pod.
deploy: label-node
	@missing=0; \
	for s in webcam-hmac:secret webcam-broker-tokens:broker-init; do \
	  name=$${s%%:*}; target=$${s##*:}; \
	  if ! $(KUBECTL) get secret $$name >/dev/null 2>&1; then \
	    echo "missing secret $$name: run 'make $$target' first"; missing=1; \
	  fi; \
	done; \
	[ $$missing -eq 0 ]
	$(KUBECTL) apply -f k8s/configmap.yaml
	$(KUBECTL) apply -f k8s/deployment.yaml
	$(KUBECTL) apply -f k8s/service.yaml

status:
	$(KUBECTL) get deployment/webcam service/webcam-broker-service
	$(KUBECTL) get pods -l app=webcam -o wide

C ?= streamer
logs:
	$(KUBECTL) logs deployment/webcam -c $(C) --tail=200

ingress:
	$(KUBECTL) apply -f k8s/ingress.yaml

include k8s/Makefile.broker-targets.mk
