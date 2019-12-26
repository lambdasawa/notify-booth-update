package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/kms"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-xray-sdk-go/xray"
	"github.com/gocolly/colly"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type (
	envVars struct {
		bucket                string
		key                   string
		boothURL              string
		encryptedSlackURL     string
		encryptedSlackChannel string
	}
)

func main() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetReportCaller(true)

	if err := xray.Configure(xray.Config{
		LogLevel:  "warn",
		LogFormat: `{"date":"%Date(2006-01-02T15:04:05Z07:00)","level":"%Level","msg":"%Msg"}"`,
	}); err != nil {
		log.Fatal(err)
	}

	lambda.Start(Run)
}

func Run(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			log.Error(err)
		}
	}()

	sess, err := session.NewSession()
	if err != nil {
		return fmt.Errorf("create new AWS session: %v", err)
	}
	region := aws.NewConfig().WithRegion("ap-northeast-1")

	kmsService := kms.New(sess, region)
	s3Service := s3.New(sess, region)

	xray.AWS(kmsService.Client)
	xray.AWS(s3Service.Client)

	envVars := getEnvVars()

	knownUrls, err := getObject(ctx, s3Service, envVars.bucket, envVars.key)
	if err != nil {
		return fmt.Errorf("get known urls: %v", err)
	}

	currentUrls := getURLs(ctx, envVars.boothURL)

	log.WithFields(log.Fields{
		"known":   knownUrls,
		"current": currentUrls,
	}).Info("urls")

	newURLs := getNewUrls(knownUrls, currentUrls)
	if len(newURLs) > 0 {
		if err := postSlack(
			ctx,
			kmsService,
			envVars.encryptedSlackURL,
			envVars.encryptedSlackChannel,
			envVars.boothURL,
			newURLs,
		); err != nil {
			return fmt.Errorf("post slack: %v", err)
		}

		newKnownUrls := append(knownUrls, currentUrls...)
		if err := putObject(ctx, s3Service, envVars.bucket, envVars.key, newKnownUrls); err != nil {
			return fmt.Errorf("put current urls: %v", err)
		}
	}
	log.WithFields(log.Fields{
		"knownUrls": knownUrls,
		"newUrls":   newURLs,
	}).Info("result")

	return nil
}

func getEnvVars() envVars {
	return envVars{
		bucket:                os.Getenv("S3_BUCKET"),
		key:                   os.Getenv("S3_KEY"),
		boothURL:              os.Getenv("BOOTH_URL"),
		encryptedSlackURL:     os.Getenv("ENCRYPTED_SLACK_URL"),
		encryptedSlackChannel: os.Getenv("ENCRYPTED_SLACK_CHANNEL"),
	}
}

func getObject(ctx context.Context, svc *s3.S3, bucket, key string) ([]string, error) {
	out, err := svc.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if strings.Contains(err.Error(), s3.ErrCodeNoSuchKey) {
			return nil, nil
		}
		return nil, fmt.Errorf("get object: %v", err)
	}

	urls := make([]string, 0)
	if err := json.NewDecoder(out.Body).Decode(&urls); err != nil {
		return nil, fmt.Errorf("decode json: %v", err)
	}
	defer out.Body.Close()

	return urls, nil
}

func getURLs(ctx context.Context, baseURL string) []string {
	_, seg := xray.BeginSubsegment(ctx, fmt.Sprintf("get item urls from %s", baseURL))
	defer seg.Close(nil)

	urlsChan := make(chan string)
	go func() {
		collector := colly.NewCollector()

		collector.OnHTML("a[href]", func(e *colly.HTMLElement) {
			href := e.Attr("href")
			if !strings.HasPrefix(href, "/items/") {
				return
			}

			urlsChan <- href
		})

		collector.OnRequest(func(r *colly.Request) {
			r.Headers.Add("Cache-Control", "no-cache, no-store")
		})

		collector.OnResponse(func(w *colly.Response) {
			log.WithFields(log.Fields{
				"requestHeaders":  w.Request.Headers,
				"responseHeaders": w.Headers,
				"status":          w.StatusCode,
			}).Info("onResponse")
		})

		if err := collector.Visit(baseURL); err != nil {
			log.WithField("visit", baseURL).Fatal(err)
		}

		close(urlsChan)
	}()

	uniqUrls := map[string]interface{}{}
	for u := range urlsChan {
		uniqUrls[baseURL+u] = struct{}{}
	}

	urls := make([]string, 0)
	for u := range uniqUrls {
		urls = append(urls, u)
	}

	sort.Strings(urls)

	return urls
}

func getNewUrls(known, current []string) []string {
	newUrls := make([]string, 0)
	for _, c := range current {
		isKnown := false
		for _, n := range known {
			if n == c {
				isKnown = true
				break
			}
		}
		if isKnown {
			continue
		}

		newUrls = append(newUrls, c)
	}

	return newUrls
}

func postSlack(
	ctx context.Context,
	svc *kms.KMS,
	encryptedSlackURL string,
	encryptedSlackChannel string,
	boothURL string,
	newURLs []string,
) error {
	channel, err := getKMSData(ctx, svc, encryptedSlackChannel)
	if err != nil {
		return fmt.Errorf("get KMS data: %v", err)
	}

	sb := new(strings.Builder)
	fmt.Fprintln(sb, "# Booth updated!!!")
	fmt.Fprintln(sb, "## Store URL")
	fmt.Fprintln(sb, boothURL)
	fmt.Fprintln(sb, "## New item URLs")
	for _, u := range newURLs {
		fmt.Fprintf(sb, "- %s\n", u)
	}

	req := map[string]interface{}{
		"text":        sb.String(),
		"channelName": channel,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode request: %v", err)
	}

	url, err := getKMSData(ctx, svc, encryptedSlackURL)
	if err != nil {
		return fmt.Errorf("get KMS data: %v", err)
	}

	xray.Client(http.DefaultClient)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBytes)) // #nosec
	if err != nil {
		return fmt.Errorf("post: %v", err)
	}

	if _, err := ioutil.ReadAll(resp.Body); err != nil {
		return fmt.Errorf("read response body: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("check response: %v", resp.Status)
	}

	return nil
}

func getKMSData(ctx context.Context, svc *kms.KMS, name string) (string, error) {
	dataBytes, err := base64.StdEncoding.DecodeString(name)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode KMS data as Base64")
	}

	var in = &kms.DecryptInput{
		CiphertextBlob: dataBytes,
	}
	out, err := svc.DecryptWithContext(ctx, in)
	if err != nil {
		return "", errors.Wrap(err, "failed to decrypt KMS value")
	}

	return string(out.Plaintext), nil
}

func putObject(ctx context.Context, svc *s3.S3, bucket, key string, urls []string) error {
	b := new(bytes.Buffer)
	if err := json.NewEncoder(b).Encode(urls); err != nil {
		return fmt.Errorf("encode json: %v", err)
	}

	r := bytes.NewReader(b.Bytes())
	if _, err := svc.PutObjectWithContext(ctx, &s3.PutObjectInput{
		Body:   r,
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("put object: %v", err)
	}

	return nil
}
