.PHONY: deploy

deploy:
	GOOS=linux GOARCH=amd64 go build -trimpath -o main main.go
	mkdir -p infra/lambda/
	mv main infra/lambda/
	cd infra &&\
		npm run build &&\
		npm run cdk deploy &&\
		npm run post-deploy

destroy:
	cd infra && cdk destroy
