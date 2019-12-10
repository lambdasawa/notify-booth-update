.PHONY: deploy

deploy:
	test -n "${S3_KEY}" || exit 1
	test -n "${BOOTH_URL}" || exit 1
	test -n "${SLACK_URL}" || exit 1
	test -n "${SLACK_CHANNEL}" || exit 1
	GOOS=linux GOARCH=amd64 go build -trimpath -o main main.go
	mkdir -p infra/lambda/
	mv main infra/lambda/
	cd infra && cdk deploy
