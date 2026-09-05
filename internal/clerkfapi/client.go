package clerkfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	clerkAPIVersion      = APIVersion
	maxResponseBodyBytes = 1 << 20
	clientCookieName     = "__client"
	requestTimeout       = 20 * time.Second
)

var errMissingNativeClientToken = errors.New("clerk native client token missing from response")

type Client struct {
	httpClient        *http.Client
	frontendAPIURL    string
	cliVersion        string
	nativeClientToken string
	createdSessionID  string
	origin            *url.URL
	requestIDs        []string
}

func New(frontendAPIURL, cliVersion string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(frontendAPIURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid Clerk Frontend API URL")
	}
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid Clerk Frontend API URL")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("invalid Clerk Frontend API URL")
	}

	origin := &url.URL{Scheme: strings.ToLower(parsed.Scheme), Host: strings.ToLower(parsed.Host)}
	return &Client{
		httpClient: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many Clerk redirects")
				}
				if !sameOrigin(req.URL, origin) {
					return errors.New("refusing cross-origin Clerk redirect")
				}
				query := req.URL.Query()
				query.Set("_is_native", "true")
				req.URL.RawQuery = query.Encode()
				return nil
			},
		},
		frontendAPIURL: origin.String(),
		cliVersion:     cliVersion,
		origin:         origin,
	}, nil
}

func (c *Client) StartEmailCode(ctx context.Context, email string) (*Challenge, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if strings.TrimSpace(email) == "" {
		return nil, errors.New("unable to start Clerk email sign-in")
	}

	if err := c.createNativeClient(ctx); err != nil {
		return nil, err
	}

	signIn, err := c.createSignIn(ctx, email)
	if err != nil {
		return nil, err
	}
	if signIn.Status != SignInNeedsFirstFactor {
		return nil, errors.New("unable to start Clerk email sign-in")
	}
	if signIn.CurrentTask != nil || len(signIn.Tasks) > 0 {
		return nil, errors.New("unable to start Clerk email sign-in")
	}

	challenge := Challenge{SignInID: signIn.ID}
	for _, factor := range signIn.SupportedFirstFactors {
		if factor.Strategy == "email_code" && factor.EmailAddressID != "" {
			challenge.EmailAddressID = factor.EmailAddressID
			break
		}
	}
	if challenge.SignInID == "" {
		return nil, errors.New("unable to start Clerk email sign-in")
	}
	if challenge.EmailAddressID == "" {
		return nil, errors.New("unable to start Clerk email sign-in")
	}

	if err := c.prepareFirstFactor(ctx, challenge); err != nil {
		return nil, err
	}
	return &challenge, nil
}

func (c *Client) VerifyEmailCode(ctx context.Context, challenge Challenge, code string) (*SessionProof, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if strings.TrimSpace(challenge.SignInID) == "" || strings.TrimSpace(code) == "" {
		return nil, errors.New("invalid Clerk email sign-in challenge")
	}

	signIn, attemptErr := c.attemptFirstFactor(ctx, challenge, code)
	if attemptErr != nil {
		return nil, wrapFlowError("attempt_first_factor", attemptErr)
	}

	switch signIn.Status {
	case SignInComplete:
	case SignInNeedsFirstFactor:
		return nil, wrapFlowError("validate_sign_in", errors.New("email code was incorrect or expired"))
	default:
		return nil, wrapFlowError("validate_sign_in", fmt.Errorf("unexpected sign-in state: %s", signIn.Status))
	}
	if signIn.ID != "" && signIn.ID != challenge.SignInID {
		return nil, wrapFlowError("validate_sign_in", errors.New("clerk sign-in response did not match challenge"))
	}
	if signIn.CurrentTask != nil || len(signIn.Tasks) > 0 {
		return nil, wrapFlowError("validate_sign_in", errors.New("pending sign-in task blocks CLI login"))
	}

	if signIn.CreatedSessionID == "" {
		return nil, wrapFlowError("validate_sign_in", errors.New("clerk sign-in response missing created_session_id"))
	}

	proof, err := c.sessionProof(ctx, signIn.CreatedSessionID)
	if err != nil {
		return nil, err
	}
	return proof, nil
}

// StartEmailSignUp creates a new Clerk signup and prepares exactly one email
// verification challenge. It deliberately supplies no legal acceptance,
// password, CAPTCHA, or other inferred fields: if the instance requires any
// additional interaction, the caller must fall back to browser auth.
func (c *Client) StartEmailSignUp(ctx context.Context, email string) (*SignUpChallenge, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if strings.TrimSpace(email) == "" {
		return nil, errors.New("unable to start Clerk email signup")
	}

	if err := c.createNativeClient(ctx); err != nil {
		return nil, err
	}

	signUp, err := c.createSignUp(ctx, email)
	if err != nil {
		if isExistingSignUpEmail(err) {
			return nil, errors.New("email already has a TTS Buddy account; run ttsbuddy auth email")
		}
		return nil, err
	}
	if signUp.ID == "" || signUp.Status == SignUpAbandoned {
		return nil, errors.New("unable to start Clerk email signup")
	}
	if signUp.CurrentTask != nil || len(signUp.Tasks) > 0 || len(signUp.MissingFields) > 0 {
		return nil, errors.New("email signup requires browser authentication")
	}
	if signUp.Status != SignUpMissingRequirements {
		return nil, errors.New("email signup requires browser authentication")
	}
	if !containsField(signUp.UnverifiedFields, "email_address") {
		return nil, errors.New("email signup requires browser authentication")
	}

	prepared, err := c.prepareSignUpVerification(ctx, signUp.ID)
	if err != nil {
		return nil, err
	}
	if prepared.ID != "" && prepared.ID != signUp.ID {
		return nil, errors.New("clerk signup response did not match challenge")
	}
	if prepared.Status == SignUpAbandoned || prepared.CurrentTask != nil || len(prepared.Tasks) > 0 || len(prepared.MissingFields) > 0 {
		return nil, errors.New("email signup requires browser authentication")
	}
	return &SignUpChallenge{SignUpID: signUp.ID}, nil
}

// VerifyEmailSignUp completes the pending signup challenge and mints the same
// session proof used by ordinary email login.
func (c *Client) VerifyEmailSignUp(ctx context.Context, challenge SignUpChallenge, code string) (*SessionProof, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if strings.TrimSpace(challenge.SignUpID) == "" || strings.TrimSpace(code) == "" {
		return nil, errors.New("invalid Clerk email signup challenge")
	}

	signUp, attemptErr := c.attemptSignUpVerification(ctx, challenge, code)
	if attemptErr != nil {
		return nil, wrapFlowError("attempt_verification", attemptErr)
	}
	if signUp.ID != "" && signUp.ID != challenge.SignUpID {
		return nil, wrapFlowError("validate_signup", errors.New("clerk signup response did not match challenge"))
	}
	if signUp.Status != SignUpComplete {
		if signUp.Status == SignUpMissingRequirements {
			return nil, wrapFlowError("validate_signup", errors.New("email code was incorrect or expired"))
		}
		return nil, wrapFlowError("validate_signup", fmt.Errorf("unexpected signup state: %s", signUp.Status))
	}
	if signUp.CurrentTask != nil || len(signUp.Tasks) > 0 {
		return nil, wrapFlowError("validate_signup", errors.New("pending signup task blocks CLI signup"))
	}
	if signUp.CreatedSessionID == "" {
		return nil, wrapFlowError("validate_signup", errors.New("clerk signup response missing created_session_id"))
	}

	return c.sessionProof(ctx, signUp.CreatedSessionID)
}

func (c *Client) sessionProof(ctx context.Context, sessionID string) (*SessionProof, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, wrapFlowError("validate_session", errors.New("clerk response missing created_session_id"))
	}
	c.createdSessionID = sessionID

	session, err := c.getSession(ctx, sessionID)
	if err != nil {
		return nil, wrapFlowError("get_session", err)
	}
	if session.CurrentTask != nil || len(session.Tasks) > 0 {
		return nil, wrapFlowError("validate_session", errors.New("pending session task blocks CLI login"))
	}
	if !strings.EqualFold(session.Status, "active") {
		return nil, wrapFlowError("validate_session", errors.New("inactive session cannot be exchanged"))
	}
	if session.ID == "" || session.ID != sessionID {
		return nil, wrapFlowError("validate_session", errors.New("clerk session response did not match created session"))
	}

	jwt, err := c.createSessionToken(ctx, session.ID)
	if err != nil {
		return nil, wrapFlowError("create_session_token", err)
	}
	c.createdSessionID = session.ID
	return &SessionProof{Token: jwt, SessionID: session.ID}, nil
}

func (c *Client) Cleanup(ctx context.Context) error {
	sessionID := c.createdSessionID
	defer c.Close()

	if c.nativeClientToken == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var err error
	if sessionID != "" {
		_, err = c.doRequest(ctx, http.MethodPost, "/v1/client/sessions/"+url.PathEscape(sessionID)+"/end", nil, false)
	} else {
		_, err = c.doRequest(ctx, http.MethodDelete, "/v1/client", nil, false)
	}
	return err
}

func (c *Client) Close() {
	c.nativeClientToken = ""
	c.createdSessionID = ""
	c.requestIDs = nil
}

// RequestIDs returns a copy of the server request ids observed by this client.
// They are safe, redacted correlation values for development evidence only.
func (c *Client) RequestIDs() []string {
	ids := make([]string, len(c.requestIDs))
	copy(ids, c.requestIDs)
	return ids
}

func (c *Client) createNativeClient(ctx context.Context) error {
	_, err := c.doRequest(ctx, http.MethodPost, "/v1/client", nil, true)
	return err
}

func (c *Client) createSignIn(ctx context.Context, email string) (*signInResponse, error) {
	env, err := c.doFormRequest(ctx, http.MethodPost, "/v1/client/sign_ins", url.Values{
		"identifier": {email},
	}, false)
	if err != nil {
		return nil, err
	}
	return decodeResponse[signInResponse](env)
}

func (c *Client) createSignUp(ctx context.Context, email string) (*signUpResponse, error) {
	env, err := c.doFormRequest(ctx, http.MethodPost, "/v1/client/sign_ups", url.Values{
		"email_address": {email},
	}, false)
	if err != nil {
		return nil, err
	}
	return decodeResponse[signUpResponse](env)
}

func (c *Client) prepareSignUpVerification(ctx context.Context, signUpID string) (*signUpResponse, error) {
	env, err := c.doFormRequest(ctx, http.MethodPost, "/v1/client/sign_ups/"+url.PathEscape(signUpID)+"/prepare_verification", url.Values{
		"strategy": {"email_code"},
	}, false)
	if err != nil {
		return nil, err
	}
	return decodeResponse[signUpResponse](env)
}

func (c *Client) attemptSignUpVerification(ctx context.Context, challenge SignUpChallenge, code string) (*signUpResponse, error) {
	env, err := c.doFormRequest(ctx, http.MethodPost, "/v1/client/sign_ups/"+url.PathEscape(challenge.SignUpID)+"/attempt_verification", url.Values{
		"strategy": {"email_code"},
		"code":     {code},
	}, false)
	if err != nil {
		return nil, err
	}
	if hasClerkError(env.Errors, "expired") {
		return nil, errors.New("email code expired")
	}
	if hasClerkError(env.Errors, "incorrect", "invalid") {
		return nil, errors.New("email code incorrect")
	}
	return decodeResponse[signUpResponse](env)
}

func (c *Client) prepareFirstFactor(ctx context.Context, challenge Challenge) error {
	env, err := c.doFormRequest(ctx, http.MethodPost, "/v1/client/sign_ins/"+url.PathEscape(challenge.SignInID)+"/prepare_first_factor", url.Values{
		"strategy":         {"email_code"},
		"email_address_id": {challenge.EmailAddressID},
	}, false)
	if err != nil {
		return err
	}
	signIn, err := decodeResponse[signInResponse](env)
	if err != nil {
		return err
	}
	if signIn.ID != "" && signIn.ID != challenge.SignInID {
		return errors.New("clerk sign-in response did not match challenge")
	}
	if signIn.Status != "" && signIn.Status != SignInNeedsFirstFactor {
		return errors.New("unable to start Clerk email sign-in")
	}
	if signIn.CurrentTask != nil || len(signIn.Tasks) > 0 {
		return errors.New("unable to start Clerk email sign-in")
	}
	return nil
}

func (c *Client) attemptFirstFactor(ctx context.Context, challenge Challenge, code string) (*signInResponse, error) {
	env, err := c.doFormRequest(ctx, http.MethodPost, "/v1/client/sign_ins/"+url.PathEscape(challenge.SignInID)+"/attempt_first_factor", url.Values{
		"strategy": {"email_code"},
		"code":     {code},
	}, false)
	if err != nil {
		return nil, err
	}

	signIn, decodeErr := decodeResponse[signInResponse](env)
	if decodeErr != nil {
		return nil, decodeErr
	}
	if signIn.Status == SignInNeedsFirstFactor && hasClerkError(env.Errors, "expired") {
		return nil, errors.New("email code expired")
	}
	if signIn.Status == SignInNeedsFirstFactor && hasClerkError(env.Errors, "incorrect", "invalid") {
		return nil, errors.New("email code incorrect")
	}
	return signIn, nil
}

func (c *Client) getSession(ctx context.Context, sessionID string) (*sessionResponse, error) {
	env, err := c.doRequest(ctx, http.MethodGet, "/v1/client/sessions/"+url.PathEscape(sessionID), nil, false)
	if err != nil {
		return nil, err
	}
	return decodeResponse[sessionResponse](env)
}

func (c *Client) createSessionToken(ctx context.Context, sessionID string) (string, error) {
	env, err := c.doFormRequest(ctx, http.MethodPost, "/v1/client/sessions/"+url.PathEscape(sessionID)+"/tokens", url.Values{}, false)
	if err != nil {
		return "", err
	}
	token, decodeErr := decodeToken(env)
	if decodeErr != nil {
		return "", decodeErr
	}
	if token == "" {
		return "", errors.New("clerk session token response missing jwt")
	}
	return token, nil
}

func (c *Client) doFormRequest(ctx context.Context, method, path string, form url.Values, expectToken bool) (*clerkEnvelope, error) {
	body := bytes.NewBufferString(form.Encode())
	return c.doRequest(ctx, method, path, body, expectToken)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader, expectToken bool) (*clerkEnvelope, error) {
	endpoint, err := c.nativeEndpoint(path)
	if err != nil {
		return nil, errors.New("unable to create Clerk request")
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("unable to create Clerk request")
	}
	req.Header.Set("Clerk-API-Version", clerkAPIVersion)
	req.Header.Set("User-Agent", "ttsbuddy-cli/"+c.cliVersion)

	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c.nativeClientToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.nativeClientToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Err != nil {
			return nil, urlErr.Err
		}
		return nil, errors.New("clerk request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	tokenSeen := c.captureNativeClientToken(resp)
	env, parseErr := parseEnvelope(resp)
	if parseErr != nil {
		if resp.StatusCode >= 400 {
			return nil, buildRequestError(resp, nil)
		}
		return nil, parseErr
	}
	if env.RequestID != "" {
		c.requestIDs = append(c.requestIDs, env.RequestID)
	}
	if resp.StatusCode >= 400 {
		return nil, buildRequestError(resp, env)
	}
	if expectToken && !tokenSeen {
		return nil, errMissingNativeClientToken
	}
	return env, nil
}

func (c *Client) nativeEndpoint(path string) (string, error) {
	parsed, err := url.Parse(c.frontendAPIURL + path)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("_is_native", "true")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (c *Client) captureNativeClientToken(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	if token := nativeAuthorizationToken(resp.Header.Get("Authorization")); token != "" {
		c.nativeClientToken = token
		return true
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == clientCookieName && cookie.Value != "" {
			c.nativeClientToken = cookie.Value
			return true
		}
	}
	return false
}

// nativeAuthorizationToken accepts both response forms observed across Clerk
// Frontend API deployments: a conventional "Bearer <token>" value and the
// raw token value returned by the development native-client endpoint. Requests
// always add the Bearer scheme in doRequest.
func nativeAuthorizationToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if token := bearerToken(value); token != "" {
		return token
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	return value
}

func parseEnvelope(resp *http.Response) (*clerkEnvelope, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, errors.New("unable to read Clerk response")
	}
	if len(data) > maxResponseBodyBytes {
		return nil, errors.New("clerk response too large")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return &clerkEnvelope{}, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("unable to decode Clerk response")
	}

	env := &clerkEnvelope{}
	if msg, ok := raw["request_id"]; ok {
		_ = json.Unmarshal(msg, &env.RequestID)
	}
	if msg, ok := raw["jwt"]; ok {
		_ = json.Unmarshal(msg, &env.JWT)
	}
	if msg, ok := raw["errors"]; ok {
		_ = json.Unmarshal(msg, &env.Errors)
	}
	if msg, ok := raw["response"]; ok {
		env.Response = append(env.Response[:0], msg...)
	}
	return env, nil
}

func decodeResponse[T any](env *clerkEnvelope) (*T, error) {
	if env == nil || len(env.Response) == 0 {
		return nil, errors.New("clerk response missing payload")
	}
	var out T
	if err := json.Unmarshal(env.Response, &out); err != nil {
		return nil, errors.New("unable to decode Clerk payload")
	}
	return &out, nil
}

func decodeToken(env *clerkEnvelope) (string, error) {
	if env == nil {
		return "", errors.New("clerk response missing payload")
	}
	if env.JWT != "" {
		return env.JWT, nil
	}
	value, err := decodeResponse[sessionTokenResponse](env)
	if err != nil {
		return "", err
	}
	return value.JWT, nil
}

func buildRequestError(resp *http.Response, env *clerkEnvelope) error {
	reqErr := &RequestError{
		StatusCode:        resp.StatusCode,
		RetryAfterSeconds: retryAfterSeconds(resp.Header.Get("Retry-After")),
	}
	if env != nil {
		reqErr.RequestID = env.RequestID
		if len(env.Errors) > 0 {
			reqErr.Code = env.Errors[0].Code
		}
	}
	return reqErr
}

func retryAfterSeconds(value string) int {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 {
		return 0
	}
	return seconds
}

func hasClerkError(errs []clerkErrorResponse, fragments ...string) bool {
	for _, item := range errs {
		candidate := strings.ToLower(item.Code + " " + item.Message + " " + item.LongMessage)
		for _, fragment := range fragments {
			if strings.Contains(candidate, strings.ToLower(fragment)) {
				return true
			}
		}
	}
	return false
}

func containsField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func isExistingSignUpEmail(err error) bool {
	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		return false
	}
	code := strings.ToLower(requestErr.Code)
	return strings.Contains(code, "identifier_exists") ||
		strings.Contains(code, "email_address_exists") ||
		strings.Contains(code, "email_exists") ||
		strings.Contains(code, "user_exists")
}

func bearerToken(value string) string {
	if value == "" {
		return ""
	}
	parts := strings.SplitN(value, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func sameOrigin(candidate, origin *url.URL) bool {
	if candidate == nil || origin == nil {
		return false
	}
	return strings.EqualFold(candidate.Scheme, origin.Scheme) && strings.EqualFold(candidate.Host, origin.Host)
}
