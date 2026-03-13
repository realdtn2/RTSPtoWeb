package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "rtsp_to_web_session"
	usersFileEnvVar   = "RTSPTOWEB_USERS_FILE"
)

type userRecord struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

type userFile struct {
	Users []userRecord `json:"users"`
}

// initAuth must be called once in HTTPAPIServer after the Gin engine is created.
func initAuth(router *gin.Engine) {
	secret := generateSessionSecret()
	store := cookie.NewStore(secret)
	store.Options(sessions.Options{
		Path:     "/",
		HttpOnly: true,
		Secure:   Storage.ServerHTTPS(),
		SameSite: http.SameSiteLaxMode,
		// Effectively "forever" until logout or cookie cleared (10 years).
		MaxAge: int((10 * 365 * 24 * time.Hour).Seconds()),
	})

	router.Use(sessions.Sessions(sessionCookieName, store))
}

func generateSessionSecret() []byte {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to static value only if crypto rand fails, which is extremely unlikely.
		log.WithFields(logrus.Fields{
			"module": "auth",
			"func":   "generateSessionSecret",
		}).Errorln("crypto/rand failed, falling back to static session secret:", err)
		return []byte("rtsp_to_web_fallback_session_key_please_restart")
	}
	return b
}

func getUsersFilePath() string {
	if v := os.Getenv(usersFileEnvVar); v != "" {
		return v
	}
	// Place users.json next to the main config file by default.
	if configFile == "" {
		return "users.json"
	}
	dir := filepath.Dir(configFile)
	return filepath.Join(dir, "users.json")
}

func loadUsers() (*userFile, error) {
	path := getUsersFilePath()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	bytes, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var uf userFile
	if err := json.Unmarshal(bytes, &uf); err != nil {
		return nil, err
	}
	return &uf, nil
}

func findUser(username string) (*userRecord, error) {
	users, err := loadUsers()
	if err != nil {
		return nil, err
	}
	for _, u := range users.Users {
		if strings.EqualFold(u.Username, username) {
			return &u, nil
		}
	}
	return nil, errors.New("user not found")
}

func verifyUserPassword(username, password string) bool {
	u, err := findUser(username)
	if err != nil {
		log.WithFields(logrus.Fields{
			"module": "auth",
			"func":   "verifyUserPassword",
		}).Warnln("login failed:", err)
		// Do not disclose whether user exists.
		return false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return false
	}
	return true
}

func authRequiredPage() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		if session.Get("user") == nil {
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}
		c.Next()
	}
}

func authRequiredAPI() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		if session.Get("user") == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"status":  0,
				"payload": "unauthorized",
			})
			return
		}
		c.Next()
	}
}

func HTTPAPILoginPage(c *gin.Context) {
	session := sessions.Default(c)
	if session.Get("user") != nil {
		c.Redirect(http.StatusFound, "/")
		return
	}

	c.HTML(http.StatusOK, "login.tmpl", gin.H{
		"version": time.Now().String(),
		"error":   "",
	})
}

func HTTPAPILoginPost(c *gin.Context) {
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")

	if username == "" || password == "" {
		c.HTML(http.StatusBadRequest, "login.tmpl", gin.H{
			"version": time.Now().String(),
			"error":   "Invalid username or password",
		})
		return
	}

	if !verifyUserPassword(username, password) {
		// Always use same generic message.
		c.HTML(http.StatusUnauthorized, "login.tmpl", gin.H{
			"version": time.Now().String(),
			"error":   "Invalid username or password",
		})
		return
	}

	session := sessions.Default(c)
	session.Set("user", username)
	if err := session.Save(); err != nil {
		log.WithFields(logrus.Fields{
			"module": "auth",
			"func":   "HTTPAPILoginPost",
		}).Errorln("failed to save session:", err)
		c.HTML(http.StatusInternalServerError, "login.tmpl", gin.H{
			"version": time.Now().String(),
			"error":   "Internal error, please try again",
		})
		return
	}

	c.Redirect(http.StatusFound, "/")
}

func HTTPAPILogout(c *gin.Context) {
	session := sessions.Default(c)
	session.Clear()
	_ = session.Save()
	c.Redirect(http.StatusFound, "/login")
}

