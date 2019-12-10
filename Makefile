.PHONY: deploy

deploy:
	bash ./deploy.sh

destroy:
	cd infra && cdk destroy
