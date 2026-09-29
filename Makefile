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
KUBECTL   ?= kubectl
GHCR_USER ?= $(notdir $(REGISTRY))

COMPONENTS := streamer controller broker
ARCH       := $(lastword $(subst /, ,$(PLATFORM)))
image       = $(REGISTRY)/fishcam-$(1):$(TAG)

.DEFAULT_GOAL := help
.PHONY: help login rootfs build push release test clean ghcr-pull-secret

help:
	@echo "Targets:"
	@echo "  login             podman login ghcr.io as GHCR_USER (token from GHCR_TOKEN, else prompted)"
	@echo "  rootfs            fetch and verify Alpine's root filesystem for the streamer"
	@echo "  build             build all three images for PLATFORM"
	@echo "  push              push all three images to REGISTRY"
	@echo "  release           build, then push"
	@echo "  test              go vet and go test for each component"
	@echo "  clean             remove locally built images and the fetched rootfs"
	@echo "  ghcr-pull-secret  create the cluster's ghcr-pull secret (for private packages)"
	@echo "  broker-init / broker-add-user NAME=... / broker-list-users"
	@echo
	@echo "Settings:"
	@echo "  REGISTRY=$(REGISTRY)"
	@echo "  TAG=$(TAG)"
	@echo "  PLATFORM=$(PLATFORM)"
	@echo "  GHCR_USER=$(GHCR_USER)"
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

include k8s/Makefile.broker-targets.mk
