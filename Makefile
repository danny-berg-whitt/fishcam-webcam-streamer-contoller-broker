# Build, publish and deploy the FishCam images. `make help` lists targets
# and the current settings.

# Per-deployment settings; see deploy.env.example. Command-line values win.
-include deploy.env

REGISTRY  ?= localhost
TAG       ?= latest
PLATFORM  ?= linux/arm64
PODMAN    ?= podman
GHCR_USER ?= $(notdir $(REGISTRY))

# kubectl if installed, else MicroK8s's (on the Pi).
ifeq ($(origin KUBECTL),undefined)
KUBECTL := $(shell if command -v kubectl >/dev/null 2>&1; then echo kubectl; \
             elif command -v microk8s >/dev/null 2>&1; then echo microk8s kubectl; \
             else echo kubectl; fi)
endif

# For k8s/render.sh.
export WEBCAM_HOST WEBCAM_NODE_LABEL REGISTRY TAG

# The default `localhost` suits local builds only, not push or deploy.
require_registry = @if [ "$(REGISTRY)" = localhost ]; then \
	  echo "REGISTRY isn't set: copy deploy.env.example to deploy.env and set it"; exit 1; fi

COMPONENTS := streamer controller broker
ARCH       := $(lastword $(subst /, ,$(PLATFORM)))
image       = $(REGISTRY)/fishcam-$(1):$(TAG)

.DEFAULT_GOAL := help
.PHONY: help login rootfs build push release test e2e pod-smoke clean ghcr-pull-secret \
        secret label-node deploy deploy-check manifests status logs ingress check-images

help:
	@echo "Targets:"
	@echo "  login             podman login ghcr.io as GHCR_USER (token from GHCR_TOKEN, else prompted)"
	@echo "  rootfs            fetch and verify Alpine's root filesystem for the streamer"
	@echo "  build             build all three images for PLATFORM"
	@echo "  push              push all three images to REGISTRY"
	@echo "  release           build, then push (then reports which images aren't public)"
	@echo "  check-images      check the cluster can pull each image without credentials"
	@echo "  test              go vet and go test for each component"
	@echo "  e2e               all three binaries end to end (stand-in ffmpeg/amixer)"
	@echo "  pod-smoke         run the built images as a pod, wired like the cluster's"
	@echo "  clean             remove locally built images and the fetched rootfs"
	@echo
	@echo "  secret            create the webcam-hmac secret (once; never overwrites)"
	@echo "  broker-init       create webcam-broker-tokens with one user (NAME=admin)"
	@echo "  broker-add-user NAME=...  /  broker-list-users"
	@echo "  ghcr-pull-secret  create the ghcr-pull secret (only for private packages)"
	@echo "  label-node        label the node with the webcam (done by deploy); NODE=<name> to pick it"
	@echo "  deploy            label-node, check secrets, apply configmap/deployment/service"
	@echo "  manifests         render all manifests from deploy.env into build/k8s/ to inspect"
	@echo "  status            deployment, pods and service"
	@echo "  logs              recent container logs (C=streamer|controller|broker, default streamer)"
	@echo "  ingress           publish the broker at /webcam/* (separate from deploy on purpose)"
	@echo
	@echo "Settings (deploy.env$(if $(wildcard deploy.env),, not found: see deploy.env.example)):"
	@echo "  WEBCAM_HOST=$(WEBCAM_HOST)"
	@echo "  WEBCAM_NODE_LABEL=$(WEBCAM_NODE_LABEL)"
	@echo "  REGISTRY=$(REGISTRY)"
	@echo "  TAG=$(TAG)"
	@echo "  PLATFORM=$(PLATFORM)"
	@echo "  GHCR_USER=$(GHCR_USER)"
	@echo "  KUBECTL=$(KUBECTL)"
	@echo "  images:"
	@$(foreach c,$(COMPONENTS),echo "    $(call image,$(c))";)

# Password: a classic token with write:packages (GHCR_TOKEN, else prompted).
login:
	@if user=$$($(PODMAN) login --get-login ghcr.io 2>/dev/null) && [ "$$user" = "$(GHCR_USER)" ]; then \
	  echo "Already logged in to ghcr.io as $$user."; \
	elif [ -n "$$GHCR_TOKEN" ]; then \
	  printf '%s' "$$GHCR_TOKEN" | $(PODMAN) login ghcr.io -u "$(GHCR_USER)" --password-stdin; \
	else \
	  $(PODMAN) login ghcr.io -u "$(GHCR_USER)"; \
	fi

rootfs:
	streamer/fetch-rootfs.sh $(ARCH)

# The streamer's runtime stage needs a native build (or emulation); the
# others cross-compile.
build: rootfs
	@set -e; for c in $(COMPONENTS); do \
	  echo "==> $$c"; \
	  $(PODMAN) build --platform $(PLATFORM) --tag $(REGISTRY)/fishcam-$$c:$(TAG) ./$$c; \
	done

push:
	$(require_registry)
	@set -e; for c in $(COMPONENTS); do \
	  $(PODMAN) push $(REGISTRY)/fishcam-$$c:$(TAG); \
	done

release: build push
	-@k8s/check-images.sh $(REGISTRY) $(TAG) $(COMPONENTS)

# The cluster pulls anonymously unless the ghcr-pull secret exists.
check-images:
	@k8s/check-images.sh $(REGISTRY) $(TAG) $(COMPONENTS)

test:
	@set -e; for c in $(COMPONENTS); do \
	  echo "==> $$c"; \
	  (cd $$c && go vet ./... && go test -count=1 ./...); \
	done

e2e:
	@set -e; bin=$$(mktemp -d); trap 'rm -rf "$$bin"' EXIT; \
	for c in controller broker streamer; do go build -C $$c -o "$$bin/$$c" .; done; \
	ci/e2e-smoke.sh "$$bin/controller" "$$bin/broker" "$$bin/streamer"

pod-smoke:
	PODMAN="$(PODMAN)" ci/pod-smoke.sh fishcam-pod-smoke \
	  $(call image,streamer) $(call image,controller) $(call image,broker)

clean:
	-@for c in $(COMPONENTS); do $(PODMAN) rmi $(REGISTRY)/fishcam-$$c:$(TAG) 2>/dev/null; done
	rm -rf streamer/rootfs build

# Only for private packages; the token needs only read:packages.
ghcr-pull-secret:
	@test -n "$$GHCR_TOKEN" || { echo "usage: GHCR_TOKEN=<token> make ghcr-pull-secret"; exit 1; }
	$(KUBECTL) create secret docker-registry ghcr-pull \
	  --docker-server=ghcr.io \
	  --docker-username="$(GHCR_USER)" \
	  --docker-password="$$GHCR_TOKEN"

# Never replaces an existing secret; rotation is deliberate.
secret:
	@if $(KUBECTL) get secret webcam-hmac >/dev/null 2>&1; then \
	  echo "webcam-hmac already exists; left unchanged."; \
	  echo "To rotate: $(KUBECTL) delete secret webcam-hmac && make secret && $(KUBECTL) rollout restart deployment/webcam"; \
	else \
	  $(KUBECTL) create secret generic webcam-hmac \
	    --from-literal=HMAC_SECRET="$$(openssl rand -hex 32)" && \
	  echo "created webcam-hmac"; \
	fi

# With several nodes and none labelled, NODE=<name> is required.
label-node:
	@k8s/render.sh k8s/deployment.yaml >/dev/null
	@label='$(WEBCAM_NODE_LABEL)'; \
	if [ -n "$(NODE)" ]; then \
	  $(KUBECTL) get node "$(NODE)" >/dev/null || exit 1; \
	  $(KUBECTL) label node "$(NODE)" "$$label" --overwrite; \
	  others=$$($(KUBECTL) get nodes -l "$$label" -o name | grep -vx "node/$(NODE)" || true); \
	  if [ -n "$$others" ]; then \
	    echo "warning: also labelled $$label, so the pod could be scheduled there:"; \
	    printf '  %s\n' $$others; \
	  fi; \
	  exit 0; \
	fi; \
	labelled=$$($(KUBECTL) get nodes -l "$$label" -o name); \
	if [ -n "$$labelled" ]; then \
	  echo "already labelled: $$labelled"; exit 0; \
	fi; \
	nodes=$$($(KUBECTL) get nodes -o name) || exit 1; \
	count=$$(printf '%s\n' "$$nodes" | grep -c . || true); \
	if [ "$$count" -eq 0 ]; then \
	  echo "$(KUBECTL) lists no nodes (context: $$($(KUBECTL) config current-context 2>/dev/null || echo unknown))."; \
	  echo "Is it pointed at the cluster with the webcam? Check with: $(KUBECTL) get nodes"; \
	  exit 1; \
	elif [ "$$count" -eq 1 ]; then \
	  $(KUBECTL) label $$nodes "$$label"; \
	else \
	  echo "$$count nodes and none labelled; say which has the webcam:"; \
	  printf '  %s\n' $$nodes; \
	  echo "  make label-node NODE=<node>"; \
	  exit 1; \
	fi

deploy-check:
	$(require_registry)
	@k8s/render.sh k8s/configmap.yaml k8s/deployment.yaml k8s/service.yaml >/dev/null
	@if $(KUBECTL) get secret ghcr-pull >/dev/null 2>&1; then \
	  echo "ghcr-pull secret present; pulling with its credentials, so not checking public access."; \
	else \
	  k8s/check-images.sh $(REGISTRY) $(TAG) $(COMPONENTS); \
	fi

# Missing secrets fail here, not as CreateContainerConfigError in the pod.
deploy: deploy-check label-node
	@missing=0; \
	for s in webcam-hmac:secret webcam-broker-tokens:broker-init; do \
	  name=$${s%%:*}; target=$${s##*:}; \
	  if ! $(KUBECTL) get secret $$name >/dev/null 2>&1; then \
	    echo "missing secret $$name: run 'make $$target' first"; missing=1; \
	  fi; \
	done; \
	[ $$missing -eq 0 ]
	k8s/render.sh -o build/k8s k8s/configmap.yaml k8s/deployment.yaml k8s/service.yaml
	$(KUBECTL) apply -f build/k8s/configmap.yaml -f build/k8s/deployment.yaml -f build/k8s/service.yaml

manifests:
	k8s/render.sh -o build/k8s k8s/*.yaml
	@echo "rendered into build/k8s/"

status:
	$(KUBECTL) get deployment/webcam service/webcam-broker-service
	$(KUBECTL) get pods -l app=webcam -o wide

C ?= streamer
logs:
	$(KUBECTL) logs deployment/webcam -c $(C) --tail=200

ingress:
	k8s/render.sh -o build/k8s k8s/ingress.yaml
	$(KUBECTL) apply -f build/k8s/ingress.yaml

include k8s/Makefile.broker-targets.mk
