# notify-booth-update

## deploy

```sh
$ env \
  S3_KEY=HOGE \
  BOOTH_URL=https://HOGE.booth.pm/ \
  SLACK_URL=https://hooks.slack.com/services/HOGE/FUGA/BAR \
  SLACK_CHANNEL=HOGE \
  ALERT_EMAIL=fuga@example.com \
  make deploy
```
