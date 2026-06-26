package pocket48

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"time"

	"pocket48-bot/internal/config"
)

const (
	BaseURL       = "https://pocketapi.48.cn"
	Version       = "7.1.37"
	pocketAPIHost = "pocketapi.48.cn"
	pocketAPIIP   = "138.113.114.153"
	paSecret      = "40F1065D8E71F2A2A2BBE3F6F3D8B8C9"
)

type Client struct {
	cfg    *config.Config
	client *http.Client
}

func NewClient(cfg *config.Config) *Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == pocketAPIHost+":443" {
				addr = pocketAPIIP + ":443"
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return &Client{
		cfg: cfg,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: tr,
		},
	}
}

func generatePA() string {
	timestamp := time.Now().UnixNano() / 1e6
	random := rand.Intn(10000)
	data := fmt.Sprintf("%d%d%s", timestamp, random, paSecret)
	hash := md5.Sum([]byte(data))
	hashStr := hex.EncodeToString(hash[:])
	paStr := fmt.Sprintf("%d,%d,%s,", timestamp, random, hashStr)
	return base64.StdEncoding.EncodeToString([]byte(paStr))
}

func (c *Client) getHeaders(includeToken bool) map[string]string {
	headers := map[string]string{
		"Content-Type":    "application/json;charset=utf-8",
		"Host":            "pocketapi.48.cn",
		"pa":              generatePA(),
		"User-Agent":      "PocketFans201807/7.1.37 (iPhone; iOS 26.2.1; Scale/3.00)",
		"appInfo":         fmt.Sprintf(`{"vendor":"apple","deviceId":"518CDBB6-9F41-429D-B455-AB29CDBD2BA9","appVersion":"7.1.37","appBuild":"26020801","osVersion":"26.2.1","osType":"ios","deviceName":"iPhone 14 Pro","os":"ios"}`),
		"P-Sign-Type":     "V0",
		"Accept":          "*/*",
		"Accept-Language": "zh-Hans-CN;q=1",
	}
	if includeToken && c.cfg.PocketToken != "" {
		headers["token"] = c.cfg.PocketToken
	}
	return headers
}

func (c *Client) Post(endpoint string, body interface{}) (*Response, error) {
	return c.post(endpoint, body, true)
}

func (c *Client) postWithoutToken(endpoint string, body interface{}) (*Response, error) {
	return c.post(endpoint, body, false)
}

func (c *Client) post(endpoint string, body interface{}, includeToken bool) (*Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}

	for k, v := range c.getHeaders(includeToken) {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var apiResp Response
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v, body: %s", err, string(bodyBytes))
	}

	if apiResp.Status != 200 {
		return &apiResp, &APIError{Status: apiResp.Status, Message: apiResp.Message}
	}

	//fmt.Printf("req result:%v\n", string(apiResp.Content))

	return &apiResp, nil
}

func (c *Client) SendSMS(mobile string) error {
	endpoint := BaseURL + "/user/api/v2/sms/send_sms"
	payload := map[string]string{
		"mobile":       mobile,
		"area":         "86",
		"businessCode": "1",
		"deviceToken":  "",
	}

	resp, err := c.postWithoutToken(endpoint, payload)
	if err != nil {
		return err
	}

	if resp.Status != 200 {
		return fmt.Errorf("SMS send failed: %s", resp.Message)
	}
	return nil
}

func (c *Client) LoginWithSMS(mobile, code string) error {
	endpoint := BaseURL + "/user/api/v2/login/app/app_login"

	payload := map[string]interface{}{
		"deviceToken": "",
		"loginType":   "MOBILE_SMS_CODE",
		"mobileCodeLogin": map[string]string{
			"area":   "86",
			"mobile": mobile,
			"code":   code,
		},
	}

	resp, err := c.postWithoutToken(endpoint, payload)
	if err != nil {
		return err
	}

	var content LoginResponseContent
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return err
	}

	c.cfg.UpdateToken(content.Token)
	return nil
}

func (c *Client) LoginWithPassword(mobile, encryptedPwd string) error {
	endpoint := BaseURL + "/user/api/v2/login/app/app_login"

	payload := map[string]interface{}{
		"deviceToken": "",
		"loginType":   "MOBILE_PWD",
		"loginMobile": map[string]string{
			"mobile": mobile,
			"pwd":    encryptedPwd,
		},
	}

	resp, err := c.postWithoutToken(endpoint, payload)
	if err != nil {
		return err
	}

	var content LoginResponseContent
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return err
	}

	c.cfg.UpdateToken(content.Token)
	return nil
}

func (c *Client) CheckToken() error {
	endpoint := BaseURL + "/im/api/v1/team/message/list/homeowner"
	payload := map[string]interface{}{
		"nextTime":  0,
		"serverId":  1,
		"channelId": 1,
		"limit":     1,
	}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return err
	}

	if resp.Message == "缺少token" {
		return fmt.Errorf("token missing")
	}
	return nil
}
