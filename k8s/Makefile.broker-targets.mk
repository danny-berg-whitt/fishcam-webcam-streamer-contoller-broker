# Included by the root Makefile, which sets KUBECTL.
KUBECTL ?= kubectl

# Access codes are printed once, only after their hash is stored; the code
# itself is kept nowhere. broker-init leaves an existing secret alone, so it
# is safe to re-run.


.PHONY: broker-init broker-add-user broker-list-users

broker-init:
	@set -e; \
	if $(KUBECTL) get secret webcam-broker-tokens >/dev/null 2>&1; then \
	  echo "webcam-broker-tokens already exists; left unchanged, existing access codes still work."; \
	  echo "To add someone: make broker-add-user NAME=<name>"; \
	  echo "To start over:  $(KUBECTL) delete secret webcam-broker-tokens && make broker-init"; \
	  exit 0; \
	fi; \
	name=$${NAME:-admin}; \
	token=$$(openssl rand -hex 32); \
	hash=$$(printf '%s' "$$token" | openssl dgst -sha256 | awk '{print $$NF}'); \
	tmp=$$(mktemp); trap 'rm -f "$$tmp"' EXIT; \
	python3 -c 'import json,sys; json.dump({sys.argv[1]: sys.argv[2]}, sys.stdout)' \
	  "$$hash" "$$name" > "$$tmp"; \
	$(KUBECTL) create secret generic webcam-broker-tokens --from-file=tokens.json="$$tmp" >/dev/null; \
	echo "Created webcam-broker-tokens. Give this access code to $$name;"; \
	echo "it will not be shown again:"; \
	echo "$$token"

# Replaces the secret in one apply, so a failure leaves existing users intact.
broker-add-user:
	@test -n "$(NAME)" || { echo "usage: make broker-add-user NAME=<name>"; exit 1; }
	@set -e; \
	data=$$($(KUBECTL) get secret webcam-broker-tokens -o jsonpath='{.data.tokens\.json}') \
	  || { echo "No webcam-broker-tokens secret yet: run make broker-init NAME=$(NAME) instead."; exit 1; }; \
	token=$$(openssl rand -hex 32); \
	hash=$$(printf '%s' "$$token" | openssl dgst -sha256 | awk '{print $$NF}'); \
	dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
	printf '%s' "$$data" | base64 -d > "$$dir/current.json"; \
	python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); d[sys.argv[2]]=sys.argv[3]; json.dump(d, open(sys.argv[4],"w"))' \
	  "$$dir/current.json" "$$hash" "$(NAME)" "$$dir/tokens.json"; \
	$(KUBECTL) create secret generic webcam-broker-tokens --from-file=tokens.json="$$dir/tokens.json" \
	  --dry-run=client -o yaml > "$$dir/secret.yaml"; \
	$(KUBECTL) apply -f "$$dir/secret.yaml" >/dev/null; \
	echo "Added $(NAME). Give them this access code; it will not be shown again:"; \
	echo "$$token"; \
	echo ""; \
	echo "Restart the broker container to pick up the change:"; \
	echo "  $(KUBECTL) rollout restart deployment/webcam"

broker-list-users:
	@set -e; \
	data=$$($(KUBECTL) get secret webcam-broker-tokens -o jsonpath='{.data.tokens\.json}') \
	  || { echo "No webcam-broker-tokens secret yet: run make broker-init."; exit 1; }; \
	printf '%s' "$$data" | base64 -d \
	  | python3 -c "import json,sys; print('\\n'.join(sorted(json.load(sys.stdin).values())))"; \
	echo "(usernames only shown; token hashes omitted)"
