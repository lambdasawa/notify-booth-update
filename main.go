package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/kms"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gocolly/colly"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

var (
	bucket                = os.Getenv("S3_BUCKET")
	key                   = os.Getenv("S3_KEY")
	boothUrl              = os.Getenv("BOOTH_URL")
	encryptedSlackUrl     = os.Getenv("ENCRYPTED_SLACK_URL")
	encryptedSlackChannel = os.Getenv("ENCRYPTED_SLACK_CHANNEL")
)

func init() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetReportCaller(true)
}

func main() {
	lambda.Start(run)
}

func run() error {
	sess := session.New()
	region := aws.NewConfig().WithRegion("ap-northeast-1")

	kmsService := kms.New(sess, region)
	s3Service := s3.New(sess, region)

	previousUrls, err := getObject(s3Service)
	if err != nil {
		return fmt.Errorf("get previous urls: %v", err)
	}

	currentUrls := getUrls(boothUrl)

	log.WithFields(log.Fields{
		"prev":    previousUrls,
		"current": currentUrls,
	}).Info("urls")

	isUpdated := isBoothUpdated(previousUrls, currentUrls)
	if isUpdated {
		if err := postSlack(kmsService); err != nil {
			return fmt.Errorf("post slack: %v", err)
		}

		if err := putObject(s3Service, currentUrls); err != nil {
			return fmt.Errorf("put current urls: %v", err)
		}
	}
	log.WithField("isUpdated", isUpdated).Info("isUpdated")

	return nil
}

func getObject(svc *s3.S3) ([]string, error) {
	out, err := svc.GetObject(&s3.GetObjectInput{
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

func getUrls(url string) []string {
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

		if err := collector.Visit(url); err != nil {
			log.WithField("visit", url).Fatal(err)
		}

		close(urlsChan)
	}()

	urls := make([]string, 0)
	for u := range urlsChan {
		urls = append(urls, u)
	}

	sort.Strings(urls)

	return urls
}

func isBoothUpdated(previous, current []string) bool {
	return !reflect.DeepEqual(previous, current)
}

func postSlack(svc *kms.KMS) error {
	channel, err := getKMSData(svc, encryptedSlackChannel)
	if err != nil {
		return fmt.Errorf("get KMS data: %v", err)
	}

	req := map[string]interface{}{
		"text":        fmt.Sprintf("!!! %s UPDATED !!!", boothUrl),
		"channelName": channel,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode request: %v", err)
	}

	url, err := getKMSData(svc, encryptedSlackUrl)
	if err != nil {
		return fmt.Errorf("get KMS data: %v", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBytes))
	if err != nil {
		return fmt.Errorf("post: %v", err)
	}

	if _, err := ioutil.ReadAll(resp.Body); err != nil {
		return fmt.Errorf("read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("check response: %v", resp.Status)
	}

	return nil
}

func getKMSData(svc *kms.KMS, name string) (string, error) {
	dataBytes, err := base64.StdEncoding.DecodeString(name)
	if err != nil {
		return "", errors.Wrap(err, "failed to decode KMS data as Base64")
	}

	var in = &kms.DecryptInput{
		CiphertextBlob: dataBytes,
	}
	out, err := svc.Decrypt(in)
	if err != nil {
		return "", errors.Wrap(err, "failed to decrypt KMS value")
	}

	return string(out.Plaintext), nil
}

func putObject(svc *s3.S3, urls []string) error {
	b := new(bytes.Buffer)
	if err := json.NewEncoder(b).Encode(urls); err != nil {
		return fmt.Errorf("encode json: %v", err)
	}

	r := bytes.NewReader(b.Bytes())
	if _, err := svc.PutObject(&s3.PutObjectInput{
		Body:   r,
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("put object: %v", err)
	}

	return nil
}
