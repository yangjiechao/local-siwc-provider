package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	authBase      = "https://auth.openai.com"
	resource      = "https://api.openai.com/v1"
	requiredScope = "chatgpt.tokens.use.direct"
)

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	Scope            string `json:"scope"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type Auth struct {
	store     *Store
	client    *http.Client
	refreshMu sync.Mutex
}

func newAuth(s *Store) *Auth { return &Auth{store: s, client: newHTTPClient()} }

func hasScope(scopes, required string) bool {
	for _, s := range strings.Fields(scopes) {
		if s == required {
			return true
		}
	}
	return false
}
func (a *Auth) tokenRequest(ctx context.Context, params url.Values) (TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBase+"/api/accounts/oauth/token", strings.NewReader(params.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return TokenResponse{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TokenResponse{}, err
	}
	var tr TokenResponse
	if err = json.Unmarshal(b, &tr); err != nil {
		return tr, fmt.Errorf("OAuth returned invalid JSON (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return tr, fmt.Errorf("OAuth HTTP %d: %s", resp.StatusCode, tr.Error)
	}
	if tr.AccessToken == "" {
		return tr, errors.New("OAuth returned no access token")
	}
	return tr, nil
}

func browserOpen(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func (a *Auth) login(ctx context.Context) error {
	prior := a.store.get()
	verifier, err := randomString(48)
	if err != nil {
		return err
	}
	state, err := randomString(32)
	if err != nil {
		return err
	}
	nonce, err := randomString(32)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer l.Close()
	callback := "http://127.0.0.1:" + fmt.Sprint(l.Addr().(*net.TCPAddr).Port) + "/auth/callback"
	clientID := "dynamic_agent_client"
	returning := prior.Credential != nil
	if returning {
		clientID = prior.Credential.ClientID
	}

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("ext_agent_host_id", prior.HostID)
	if !returning {
		q.Set("agent_name_hint", "Local SIWC Provider")
	}

	q.Set("redirect_uri", callback)
	q.Set("response_type", "code")
	q.Set("scope", "openid profile email offline_access resource.invoke "+requiredScope)
	q.Set("resource", resource)
	q.Set("state", state)
	q.Set("nonce", nonce)
	h := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(h[:]))
	q.Set("code_challenge_method", "S256")
	authURL := authBase + "/api/accounts/authorize?" + q.Encode()

	result := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid state", 400)
			return
		}
		if e := r.URL.Query().Get("error"); e != "" {
			http.Error(w, "Authorization declined", 400)
			select {
			case result <- fmt.Errorf("authorization rejected: %s", e):
			default:
			}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing code", 400)
			return
		}
		issuedID := r.URL.Query().Get("client_id")
		if returning {
			if issuedID != "" && issuedID != clientID {
				http.Error(w, "Client mismatch", 400)
				return
			}
		} else if !strings.HasPrefix(issuedID, "oaiapp_") {
			http.Error(w, "Registration missing issued client ID", 400)
			return
		} else {
			clientID = issuedID
		}
		reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		tokens, e := a.tokenRequest(reqCtx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {callback}, "resource": {resource}})
		if e == nil && !hasScope(tokens.Scope, requiredScope) {
			e = errors.New("ChatGPT plan usage scope not granted")
		}
		var claims OIDCClaims
		if e == nil {
			claims, e = a.verifyIDToken(reqCtx, tokens.IDToken, clientID, nonce)
		}
		if e == nil && returning && (claims.Subject != prior.Credential.Subject || claims.Issuer != prior.Credential.Issuer) {
			e = errors.New("returning account does not match registered identity")
		}
		if e == nil {
			c := &Credential{Email: claims.Email, Issuer: claims.Issuer, Subject: claims.Subject, ClientID: clientID, HostID: prior.HostID, IDToken: tokens.IDToken, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, Scopes: tokens.Scope, ExpiresAt: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)}
			e = a.store.saveCredential(c)
		}
		if e != nil {
			http.Error(w, "Authorization failed. Check CLI for details.", 400)
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "Continue with ChatGPT: authorization complete. You may close this tab.")
		}
		select {
		case result <- e:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(l)
	defer server.Shutdown(context.Background())
	fmt.Println("Continue with ChatGPT: open this official authorization link in your browser:\n" + authURL)
	if e := browserOpen(authURL); e != nil {
		fmt.Println("Browser not opened automatically; use the URL above.")
	}
	select {
	case e := <-result:
		return e
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Minute):
		return errors.New("sign-in timed out")
	}
}

type OIDCClaims struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	Email     string          `json:"email"`
	Audience  json.RawMessage `json:"aud"`
	Nonce     string          `json:"nonce"`
	Expires   int64           `json:"exp"`
	NotBefore int64           `json:"nbf"`
}
type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Use string `json:"use"`
}

func decodeB64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
func audienceMatches(data json.RawMessage, id string) bool {
	var single string
	if json.Unmarshal(data, &single) == nil {
		return single == id
	}
	var many []string
	if json.Unmarshal(data, &many) == nil {
		for _, v := range many {
			if v == id {
				return true
			}
		}
	}
	return false
}
func (a *Auth) verifyIDToken(ctx context.Context, raw, clientID, nonce string) (OIDCClaims, error) {
	var claims OIDCClaims
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return claims, errors.New("invalid ID token format")
	}
	headerBytes, err := decodeB64(parts[0])
	if err != nil {
		return claims, err
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err = json.Unmarshal(headerBytes, &header); err != nil {
		return claims, err
	}
	if header.Alg != "RS256" || header.Kid == "" {
		return claims, errors.New("ID token must use RS256 with kid")
	}
	// Fetch OpenAI's OIDC metadata, never trust a JWT-supplied key URL.
	discoveryReq, _ := http.NewRequestWithContext(ctx, "GET", authBase+"/.well-known/openid-configuration", nil)
	discResp, err := a.client.Do(discoveryReq)
	if err != nil {
		return claims, err
	}
	defer discResp.Body.Close()
	if discResp.StatusCode != 200 {
		return claims, fmt.Errorf("OIDC discovery HTTP %d", discResp.StatusCode)
	}
	var conf struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err = json.NewDecoder(io.LimitReader(discResp.Body, 1<<20)).Decode(&conf); err != nil {
		return claims, err
	}
	if conf.Issuer != authBase {
		return claims, errors.New("unexpected OIDC issuer")
	}
	jwksURL, err := url.Parse(conf.JWKSURI)
	if err != nil {
		return claims, err
	}
	if jwksURL.Scheme != "https" || jwksURL.Host != "auth.openai.com" || jwksURL.User != nil {
		return claims, errors.New("untrusted JWKS endpoint")
	}
	jwkReq, _ := http.NewRequestWithContext(ctx, "GET", jwksURL.String(), nil)
	resp, err := a.client.Do(jwkReq)
	if err != nil {
		return claims, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return claims, fmt.Errorf("JWKS HTTP %d", resp.StatusCode)
	}
	var keys struct {
		Keys []jwk `json:"keys"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&keys); err != nil {
		return claims, err
	}
	signature, err := decodeB64(parts[2])
	if err != nil {
		return claims, err
	}
	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	valid := false
	for _, k := range keys.Keys {
		if k.Kid != header.Kid || k.Kty != "RSA" || (k.Alg != "" && k.Alg != "RS256") || k.N == "" || k.E == "" {
			continue
		}
		nb, e1 := decodeB64(k.N)
		eb, e2 := decodeB64(k.E)
		if e1 != nil || e2 != nil {
			continue
		}
		exponent := new(big.Int).SetBytes(eb).Int64()
		if exponent < 3 || exponent > 1<<31-1 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(exponent)}
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, signed[:], signature) == nil {
			valid = true
			break
		}
	}
	if !valid {
		return claims, errors.New("ID token signature validation failed")
	}
	payload, err := decodeB64(parts[1])
	if err != nil {
		return claims, err
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return claims, err
	}
	now := time.Now().Unix()
	if claims.Issuer != authBase || claims.Subject == "" || !audienceMatches(claims.Audience, clientID) || claims.Expires <= now || claims.NotBefore > now+60 {
		return claims, errors.New("invalid ID token issuer, subject, audience or lifetime")
	}
	if nonce != "" && claims.Nonce != nonce {
		return claims, errors.New("ID token nonce mismatch")
	}
	return claims, nil
}

func (a *Auth) accessToken(ctx context.Context) (string, error) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	s := a.store.get()
	c := s.Credential
	if c == nil || c.AccessToken == "" {
		return "", errors.New("not signed in; run 'login'")
	}
	if !hasScope(c.Scopes, requiredScope) {
		return "", errors.New("ChatGPT plan usage permission missing; reauthorize")
	}
	if time.Until(c.ExpiresAt) > 90*time.Second {
		return c.AccessToken, nil
	}
	if c.RefreshToken == "" {
		return "", errors.New("refresh token missing; run 'login'")
	}
	tr, err := a.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}, "resource": {resource}})
	if err != nil {
		return "", fmt.Errorf("refresh failed; retry or login: %w", err)
	}
	if tr.Scope != "" {
		if !hasScope(tr.Scope, requiredScope) {
			return "", errors.New("refresh no longer grants plan usage")
		}
		c.Scopes = tr.Scope
	}
	c.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		c.RefreshToken = tr.RefreshToken
	}
	c.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if err := a.store.saveCredential(c); err != nil {
		return "", err
	}
	return c.AccessToken, nil
}

func (a *Auth) logout(ctx context.Context) error {
	c := a.store.get().Credential
	if c == nil || c.RefreshToken == "" {
		return nil
	}
	// Resolve the revocation endpoint from trusted OpenAI discovery metadata.
	req, err := http.NewRequestWithContext(ctx, "GET", authBase+"/.well-known/openid-configuration", nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("OIDC discovery HTTP %d", response.StatusCode)
	}
	var meta struct {
		RevocationEndpoint string `json:"revocation_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&meta); err != nil {
		return err
	}
	endpoint, err := url.Parse(meta.RevocationEndpoint)
	if err != nil {
		return err
	}
	if endpoint.Scheme != "https" || endpoint.Host != "auth.openai.com" || endpoint.User != nil {
		return errors.New("invalid OIDC revocation endpoint")
	}
	params := url.Values{"token": {c.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {c.ClientID}}
	req, err = http.NewRequestWithContext(ctx, "POST", endpoint.String(), strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response2, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer response2.Body.Close()
	if response2.StatusCode != 200 {
		return fmt.Errorf("revocation HTTP %d; local tokens preserved", response2.StatusCode)
	}
	// Retain the issued client_id and validated subject for future reauthorization.
	c.AccessToken = ""
	c.RefreshToken = ""
	c.IDToken = ""
	c.ExpiresAt = time.Time{}
	return a.store.saveCredential(c)
}
