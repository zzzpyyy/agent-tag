package tag

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	authCookieName  = "agent_tag_session"
	passwordRounds  = 120000
	sessionLifetime = 30 * 24 * time.Hour
)

var usernamePattern = regexp.MustCompile(`^[\p{L}\p{N}_.-]{2,40}$`)

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func pbkdf2SHA256(password, salt []byte, rounds, size int) []byte {
	result := make([]byte, 0, size)
	for block := 1; len(result) < size; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		value := append([]byte(nil), u...)
		for iteration := 1; iteration < rounds; iteration++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for index := range value {
				value[index] ^= u[index]
			}
		}
		result = append(result, value...)
	}
	return result[:size]
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest := pbkdf2SHA256([]byte(password), salt, passwordRounds, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordRounds, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	rounds, err := strconv.Atoi(parts[1])
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[2])
	want, digestErr := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || saltErr != nil || digestErr != nil || rounds < 1 || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, rounds, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func sessionTokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: authCookieName, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func findUserByName(state *State, username string) *User {
	for index := range state.Users {
		if strings.EqualFold(state.Users[index].Username, username) {
			return &state.Users[index]
		}
	}
	return nil
}

func findUserByID(state *State, id string) *User {
	for index := range state.Users {
		if state.Users[index].ID == id {
			return &state.Users[index]
		}
	}
	return nil
}

func (s *WebServer) newLoginSession(state *State, userID string) (string, time.Time, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(sessionLifetime)
	active := state.LoginSessions[:0]
	for _, session := range state.LoginSessions {
		deadline, parseErr := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		if parseErr == nil && deadline.After(now) {
			active = append(active, session)
		}
	}
	state.LoginSessions = append(active, LoginSession{TokenHash: sessionTokenHash(token), UserID: userID, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expires.Format(time.RFC3339Nano)})
	return token, expires, nil
}

func (s *WebServer) authenticate(r *http.Request) (User, bool) {
	cookie, err := r.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		return User{}, false
	}
	state, err := s.store.Read()
	if err != nil {
		return User{}, false
	}
	tokenHash := sessionTokenHash(cookie.Value)
	now := time.Now().UTC()
	for _, session := range state.LoginSessions {
		if subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(tokenHash)) != 1 {
			continue
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		if parseErr != nil || !expires.After(now) {
			return User{}, false
		}
		user := findUserByID(&state, session.UserID)
		if user != nil {
			return *user, true
		}
	}
	return User{}, false
}

func publicUser(user User) map[string]any {
	return map[string]any{"id": user.ID, "username": user.Username}
}

func (s *WebServer) register(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decode(r, &input) != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if !usernamePattern.MatchString(input.Username) {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "账号需为 2-40 位字母、数字、中文、点、下划线或横线"})
		return
	}
	if len(input.Password) < 8 || len(input.Password) > 200 {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "密码长度需为 8-200 位"})
		return
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "无法创建账号"})
		return
	}
	var user User
	var token string
	var expires time.Time
	err = s.store.Update(func(state *State) error {
		if findUserByName(state, input.Username) != nil {
			return fmt.Errorf("账号已存在")
		}
		firstUser := len(state.Users) == 0
		defaults := DiscussionSettings{ReviewRounds: 1, SkillMode: "auto", AllowSkillExecution: true, SkillPermissions: SkillPermissions{Shell: true, Network: true, Write: true}}
		if firstUser {
			defaults = state.Defaults
		}
		user = User{ID: NextID(state, "user"), Username: input.Username, PasswordHash: passwordHash, Defaults: defaults, CreatedAt: Now()}
		state.Users = append(state.Users, user)
		if firstUser {
			for index := range state.Conversations {
				if state.Conversations[index].OwnerID == "" {
					state.Conversations[index].OwnerID = user.ID
				}
			}
		}
		token, expires, err = s.newLoginSession(state, user.ID)
		return err
	})
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	setSessionCookie(w, token, expires)
	jsonResponse(w, http.StatusCreated, map[string]any{"user": publicUser(user)})
}

func (s *WebServer) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decode(r, &input) != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	state, err := s.store.Read()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "无法登录"})
		return
	}
	user := findUserByName(&state, strings.TrimSpace(input.Username))
	if user == nil || !verifyPassword(user.PasswordHash, input.Password) {
		jsonResponse(w, http.StatusUnauthorized, map[string]string{"error": "账号或密码错误"})
		return
	}
	var token string
	var expires time.Time
	err = s.store.Update(func(state *State) error {
		var createErr error
		token, expires, createErr = s.newLoginSession(state, user.ID)
		return createErr
	})
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "无法登录"})
		return
	}
	setSessionCookie(w, token, expires)
	jsonResponse(w, http.StatusOK, map[string]any{"user": publicUser(*user)})
}

func (s *WebServer) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(authCookieName)
	if cookie != nil && cookie.Value != "" {
		tokenHash := sessionTokenHash(cookie.Value)
		_ = s.store.Update(func(state *State) error {
			kept := state.LoginSessions[:0]
			for _, session := range state.LoginSessions {
				if subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(tokenHash)) != 1 {
					kept = append(kept, session)
				}
			}
			state.LoginSessions = kept
			return nil
		})
	}
	clearSessionCookie(w)
	jsonResponse(w, http.StatusOK, map[string]bool{"loggedOut": true})
}
