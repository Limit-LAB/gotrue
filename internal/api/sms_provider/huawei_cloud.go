package sms_provider

import (
	"net/http"
	"net/url"

	"bytes"
	"crypto/hmac"
	"crypto/sha256"

	"fmt"
	"io"

	"sort"
	"strings"
	"time"

	"github.com/supabase/gotrue/internal/conf"
)

// from https://obs.cn-north-1.myhuaweicloud.com/apig-sdk/APIGW-go-sdk.zip signal.go
const (
	DateFormat           = "20060102T150405Z"
	SignAlgorithm        = "SDK-HMAC-SHA256"
	HeaderXDateTime      = "X-Sdk-Date"
	HeaderXHost          = "host"
	HeaderXAuthorization = "Authorization"
	HeaderXContentSha256 = "X-Sdk-Content-Sha256"
)

func hmacsha256(keyByte []byte, dataStr string) ([]byte, error) {
	hm := hmac.New(sha256.New, []byte(keyByte))
	if _, err := hm.Write([]byte(dataStr)); err != nil {
		return nil, err
	}
	return hm.Sum(nil), nil
}

// Build a CanonicalRequest from a regular request string
func CanonicalRequest(request *http.Request, signedHeaders []string) (string, error) {
	var hexencode string
	var err error
	if hex := request.Header.Get(HeaderXContentSha256); hex != "" {
		hexencode = hex
	} else {
		bodyData, err := RequestPayload(request)
		if err != nil {
			return "", err
		}
		hexencode, err = HexEncodeSHA256Hash(bodyData)
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s", request.Method, request.URL.Path, request.URL.RawQuery, CanonicalHeaders(request, signedHeaders), strings.Join(signedHeaders, ";"), hexencode), err
}

// CanonicalHeaders
func CanonicalHeaders(request *http.Request, signerHeaders []string) string {
	var canonicalHeaders []string
	header := make(map[string][]string)
	for k, v := range request.Header {
		header[strings.ToLower(k)] = v
	}
	for _, key := range signerHeaders {
		value := header[key]
		if strings.EqualFold(key, HeaderXHost) {
			value = []string{request.Host}
		}
		sort.Strings(value)
		for _, v := range value {
			canonicalHeaders = append(canonicalHeaders, key+":"+strings.TrimSpace(v))
		}
	}
	return fmt.Sprintf("%s\n", strings.Join(canonicalHeaders, "\n"))
}

// SignedHeaders
func SignedHeaders(r *http.Request) []string {
	var signedHeaders []string
	for key := range r.Header {
		signedHeaders = append(signedHeaders, strings.ToLower(key))
	}
	sort.Strings(signedHeaders)
	return signedHeaders
}

// RequestPayload
func RequestPayload(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return []byte(""), nil
	}
	bodyByte, err := io.ReadAll(request.Body)
	if err != nil {
		return []byte(""), err
	}
	request.Body = io.NopCloser(bytes.NewBuffer(bodyByte))
	return bodyByte, err
}

// Create a "String to Sign".
func StringToSign(canonicalRequest string, t time.Time) (string, error) {
	hashStruct := sha256.New()
	_, err := hashStruct.Write([]byte(canonicalRequest))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\n%s\n%x",
		SignAlgorithm, t.UTC().Format(DateFormat), hashStruct.Sum(nil)), nil
}

// Create the HWS Signature.
func SignStringToSign(stringToSign string, signingKey []byte) (string, error) {
	hmsha, err := hmacsha256(signingKey, stringToSign)
	return fmt.Sprintf("%x", hmsha), err
}

// HexEncodeSHA256Hash returns hexcode of sha256
func HexEncodeSHA256Hash(body []byte) (string, error) {
	hashStruct := sha256.New()
	if len(body) == 0 {
		body = []byte("")
	}
	_, err := hashStruct.Write(body)
	return fmt.Sprintf("%x", hashStruct.Sum(nil)), err
}

// Get the finalized value for the "Authorization" header. The signature parameter is the output from SignStringToSign
func AuthHeaderValue(signatureStr, accessKeyStr string, signedHeaders []string) string {
	return fmt.Sprintf("%s Access=%s, SignedHeaders=%s, Signature=%s", SignAlgorithm, accessKeyStr, strings.Join(signedHeaders, ";"), signatureStr)
}

// Signature HWS meta
type Signer struct {
	Key    string
	Secret string
}

// SignRequest set Authorization header
func (s *Signer) Sign(request *http.Request) error {
	var t time.Time
	var err error
	var date string
	if date = request.Header.Get(HeaderXDateTime); date != "" {
		t, err = time.Parse(DateFormat, date)
	}
	if err != nil || date == "" {
		t = time.Now()
		request.Header.Set(HeaderXDateTime, t.UTC().Format(DateFormat))
	}
	signedHeaders := SignedHeaders(request)
	canonicalRequest, err := CanonicalRequest(request, signedHeaders)
	if err != nil {
		return err
	}
	stringToSignStr, err := StringToSign(canonicalRequest, t)
	if err != nil {
		return err
	}
	signatureStr, err := SignStringToSign(stringToSignStr, []byte(s.Secret))
	if err != nil {
		return err
	}
	authValueStr := AuthHeaderValue(signatureStr, s.Key, signedHeaders)
	request.Header.Set(HeaderXAuthorization, authValueStr)
	return nil
}

type HuaweiCloudProvider struct {
	Config *conf.HuaweiCloudProviderConfiguration
}

// SendMessage implements SmsProvider.
func (p *HuaweiCloudProvider) SendMessage(phone string, message string, channel string, otp string) (string, error) {
	params := buildRequestParams(p.Config.ChannelNumber, phone, message, otp, p.Config.ChannelName)
	req, err := http.NewRequest("POST", p.Config.ApiPath, bytes.NewBuffer([]byte(params)))
	if err != nil {
		return "", err
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	s := Signer{
		Key:    p.Config.ApiKey,
		Secret: p.Config.ApiSecret,
	}
	s.Sign(req)

	client := &http.Client{Timeout: defaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func buildRequestParams(sender, receiver, templateId, otp, signature string) string {
	return fmt.Sprintf("from=%s&to=%s&templateId=%s&templateParas=%s&signature=%s",
		url.QueryEscape(sender), url.QueryEscape(receiver),
		url.QueryEscape(templateId), url.QueryEscape(fmt.Sprintf("[\"%s\"]", otp)),
		url.QueryEscape(signature))
}

func NewHuaweiCloudProvider(config conf.HuaweiCloudProviderConfiguration) (SmsProvider, error) {
	return &HuaweiCloudProvider{
		Config: &config,
	}, nil
}
