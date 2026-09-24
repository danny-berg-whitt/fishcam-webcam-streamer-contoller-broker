# Add to the project's existing Makefile.
#
# broker-init: creates the tokens secret with a single user, "admin".
# broker-add-user: adds one more user (there are only ever one or two).
# Both print the raw token once; the broker never sees or stores it,
# only its sha256 hash, so this is the only place it's recoverable.

TOKENS_FILE := /tmp/fishcam-broker-tokens.json

.PHONY: broker-init broker-add-user broker-list-users

broker-init:
	@NAME=$${NAME:-admin}; \
	TOKEN=$$(openssl rand -hex 32); \
	HASH=$$(printf '%s' "$$TOKEN" | sha256sum | awk '{print $$1}'); \
	echo "{\"$$HASH\":\"$$NAME\"}" > $(TOKENS_FILE); \
	kubectl create secret generic webcam-broker-tokens \
	  --from-file=tokens.json=$(TOKENS_FILE); \
	rm -f $(TOKENS_FILE); \
	echo ""; \
	echo "Give this access code to $$NAME — it will not be shown again:"; \
	echo "$$TOKEN"

# Usage: make broker-add-user NAME=bob
broker-add-user:
	@test -n "$(NAME)" || (echo "usage: make broker-add-user NAME=<name>"; exit 1)
	@TOKEN=$$(openssl rand -hex 32); \
	HASH=$$(printf '%s' "$$TOKEN" | sha256sum | awk '{print $$1}'); \
	kubectl get secret webcam-broker-tokens -o jsonpath='{.data.tokens\.json}' \
	  | base64 -d > $(TOKENS_FILE); \
	python3 -c "import json,sys; d=json.load(open('$(TOKENS_FILE)')); d['$$HASH']='$(NAME)'; json.dump(d, open('$(TOKENS_FILE)','w'))"; \
	kubectl delete secret webcam-broker-tokens; \
	kubectl create secret generic webcam-broker-tokens \
	  --from-file=tokens.json=$(TOKENS_FILE); \
	rm -f $(TOKENS_FILE); \
	echo ""; \
	echo "Give this access code to $(NAME) — it will not be shown again:"; \
	echo "$$TOKEN"; \
	echo ""; \
	echo "Restart the broker container to pick up the change:"; \
	echo "  kubectl rollout restart deployment/webcam"

broker-list-users:
	@kubectl get secret webcam-broker-tokens -o jsonpath='{.data.tokens\.json}' \
	  | base64 -d | python3 -m json.tool | grep -v '^\s*"[0-9a-f]\{64\}"' || true
	@echo "(usernames only shown; token hashes omitted)"
