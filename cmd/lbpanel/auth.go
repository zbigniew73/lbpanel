package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const cookieName = "lbpanel_session"

type contextKey string

const ctxUser contextKey = "user"

func hashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func generateJWT(username string) (string, error) {
	secret := dbGetSetting("jwt_secret")
	if secret == "" {
		return "", fmt.Errorf("jwt_secret not set")
	}
	claims := jwt.MapClaims{
		"sub": username,
		"exp": time.Now().Add(8 * time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func validateJWT(tokenStr string) (string, error) {
	secret := dbGetSetting("jwt_secret")
	if secret == "" {
		return "", fmt.Errorf("jwt_secret not set")
	}
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("invalid claims")
	}
	sub, ok := claims["sub"].(string)
	if !ok {
		return "", fmt.Errorf("invalid sub")
	}
	return sub, nil
}

func setSessionCookie(w http.ResponseWriter, username string) error {
	token, err := generateJWT(username)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,                  // true — wymagane przez SameSite=None
		SameSite: http.SameSiteNoneMode, // None — działa przez Caddy proxy
		MaxAge:   8 * 3600,
	})
	return nil
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	})
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		username, err := validateJWT(cookie.Value)
		if err != nil {
			clearSessionCookie(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func agentAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-LBPanel-Key")
		if key == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		node, err := dbGetNodeByKey(key)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		dbUpdateNodeStatus(node.ID, "online")
		ctx := context.WithValue(r.Context(), contextKey("node"), node)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ensureAdminExists() {
	existing := dbGetSetting("admin_password_hash")
	if existing != "" {
		return
	}
	hash, err := hashPassword("lbadmin")
	if err != nil {
		panic(err)
	}
	dbSetSetting("admin_username", "lbadmin")
	dbSetSetting("admin_password_hash", hash)

	b := make([]byte, 32)
	rand.Read(b)
	dbSetSetting("jwt_secret", hex.EncodeToString(b))
}
