SHELL := /bin/bash
ENV := $(abspath $(CURDIR)/../.env)

# Terraform reads AWS_* from the environment, and the repo-root .env is where
# this project already keeps secrets. Sourcing beats a second copy of the keys.
define with_env
set -a && source $(ENV) && set +a &&
endef

.PHONY: init plan apply destroy deploy check

init:
	$(with_env) cd terraform && terraform init

plan:
	$(with_env) cd terraform && terraform plan

apply:
	$(with_env) cd terraform && terraform apply

destroy:
	$(with_env) cd terraform && terraform destroy

deploy:
	cd ansible && ansible-galaxy collection install -r requirements.yml && ansible-playbook playbook.yml

check:
	@for h in $$(grep -E '^(api|market)_domain' ansible/inventory.ini | cut -d= -f2); do \
		printf '%-32s %s\n' "$$h" "$$(curl -s -o /dev/null -w '%{http_code}' https://$$h/health)"; \
	done
