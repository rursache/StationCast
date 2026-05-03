package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	recaptchaVerifyURL = "https://www.google.com/recaptcha/api/siteverify"
	recaptchaAction    = "login"
	recaptchaMinScore  = 0.5
)

type recaptchaResp struct {
	Success    bool     `json:"success"`
	Score      float64  `json:"score"`
	Action     string   `json:"action"`
	Hostname   string   `json:"hostname"`
	ErrorCodes []string `json:"error-codes"`
}

// verifyRecaptcha validates a reCAPTCHA v3 token against Google's siteverify
// endpoint. It enforces the expected action and a minimum score. It returns
// true unconditionally when the secret is empty so deployments without
// reCAPTCHA configured pass through
func verifyRecaptcha(ctx context.Context, secret, token, remoteIP string) bool {
	if secret == "" {
		return true
	}
	if token == "" {
		return false
	}
	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, recaptchaVerifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out recaptchaResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false
	}
	if !out.Success {
		return false
	}
	if out.Action != "" && out.Action != recaptchaAction {
		return false
	}
	return out.Score >= recaptchaMinScore
}
