package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// OpenID Connect login (Authentik, Authelia, Keycloak, Pocket ID, ...).
// Authorization code flow with PKCE. Identity comes from the userinfo endpoint,
// fetched server-to-server over TLS, so no ID token parsing or JWT library is needed.

type oidcMeta struct {
	Issuer   string `json:"issuer"`
	Auth     string `json:"authorization_endpoint"`
	Token    string `json:"token_endpoint"`
	UserInfo string `json:"userinfo_endpoint"`
}

type pendingLogin struct {
	verifier, redirect string
	at                 time.Time
}

var (
	oidcMu      sync.Mutex
	oidcCache   = map[string]oidcMeta{}
	oidcPending = map[string]pendingLogin{}
)

func discover(issuer string) (oidcMeta, error) {
	oidcMu.Lock()
	m, ok := oidcCache[issuer]
	oidcMu.Unlock()
	if ok {
		return m, nil
	}
	resp, err := httpc.Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return m, fmt.Errorf("%s/.well-known/openid-configuration: %s (is the issuer URL right?)", issuer, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return m, err
	}
	if m.Auth == "" || m.Token == "" || m.UserInfo == "" {
		return m, errors.New("provider config is missing the authorization, token or userinfo endpoint")
	}
	oidcMu.Lock()
	oidcCache[issuer] = m
	oidcMu.Unlock()
	return m, nil
}

func randB64(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// redirectURI is what to register in the provider: the address the browser used to reach us.
func redirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/oidc/callback"
}

func oidcError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!doctype html><meta name=viewport content="width=device-width"><body style="font:16px system-ui;padding:2em">
<h2>Sign-in failed</h2><p>%s</p><p><a href="/">Back to Shrinkarr</a></p>`, htmlEscape(msg))
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func oidcLogin(w http.ResponseWriter, r *http.Request) {
	s := cfg()
	if s.OIDCIssuer == "" {
		oidcError(w, "SSO isn't set up.")
		return
	}
	m, err := discover(s.OIDCIssuer)
	if err != nil {
		oidcError(w, err.Error())
		return
	}
	state, verifier := randB64(24), randB64(48)
	ru := redirectURI(r)
	oidcMu.Lock()
	for k, p := range oidcPending {
		if time.Since(p.at) > 10*time.Minute {
			delete(oidcPending, k)
		}
	}
	oidcPending[state] = pendingLogin{verifier: verifier, redirect: ru, at: time.Now()}
	oidcMu.Unlock()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {s.OIDCClientID},
		"redirect_uri":          {ru},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(m.Auth, "?") {
		sep = "&"
	}
	http.Redirect(w, r, m.Auth+sep+q.Encode(), http.StatusFound)
}

type userInfo struct {
	Sub      string   `json:"sub"`
	Username string   `json:"preferred_username"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
}

// allowed reports whether the user matches the allow list. Empty list: anyone the provider lets in.
func (u userInfo) allowed(list string) bool {
	if strings.TrimSpace(list) == "" {
		return true
	}
	ids := []string{strings.ToLower(u.Username), strings.ToLower(u.Email)}
	for _, g := range u.Groups {
		ids = append(ids, strings.ToLower(g))
	}
	for _, a := range strings.Split(strings.ToLower(list), ",") {
		if a = strings.TrimSpace(a); a != "" && slices.Contains(ids, a) {
			return true
		}
	}
	return false
}

func oidcCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		oidcError(w, "The provider said: "+e+" "+q.Get("error_description"))
		return
	}
	oidcMu.Lock()
	p, ok := oidcPending[q.Get("state")]
	delete(oidcPending, q.Get("state"))
	oidcMu.Unlock()
	if !ok || time.Since(p.at) > 10*time.Minute {
		oidcError(w, "This sign-in link expired or was already used. Try again.")
		return
	}
	s := cfg()
	m, err := discover(s.OIDCIssuer)
	if err != nil {
		oidcError(w, err.Error())
		return
	}
	resp, err := httpc.PostForm(m.Token, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {q.Get("code")},
		"redirect_uri":  {p.redirect},
		"client_id":     {s.OIDCClientID},
		"client_secret": {s.OIDCSecret},
		"code_verifier": {p.verifier},
	})
	if err != nil {
		oidcError(w, "Token request failed: "+err.Error())
		return
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Desc        string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if tok.AccessToken == "" {
		oidcError(w, fmt.Sprintf("The provider refused the login (%s %s). Check the client ID, secret and redirect URI.", tok.Error, tok.Desc))
		return
	}
	req, _ := http.NewRequest("GET", m.UserInfo, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err = httpc.Do(req)
	if err != nil {
		oidcError(w, "Userinfo request failed: "+err.Error())
		return
	}
	var u userInfo
	err = json.NewDecoder(resp.Body).Decode(&u)
	resp.Body.Close()
	if err != nil || u.Sub == "" {
		oidcError(w, "The provider didn't return who you are.")
		return
	}
	if !u.allowed(s.OIDCAllowed) {
		oidcError(w, fmt.Sprintf("%s isn't on Shrinkarr's allowed list.", firstNonEmpty(u.Username, u.Email, u.Sub)))
		return
	}
	newSession(w, firstNonEmpty(u.Username, u.Email, u.Sub))
	http.Redirect(w, r, "/", http.StatusFound)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
