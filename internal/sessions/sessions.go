package sessions

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/omrico/backbone/internal/auth"
	"github.com/omrico/backbone/internal/config"
	bberr "github.com/omrico/backbone/internal/errors"
	"github.com/omrico/backbone/internal/k8s"
	"github.com/omrico/backbone/internal/misc"

	ginsession "github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
)

type SessionManager struct {
	Cfg         *config.Config
	SyncClient  *k8s.Client
	CookieStore ginsession.Store
}

func (sm *SessionManager) Init(r *gin.Engine) {
	// cookie store
	sm.CookieStore = cookie.NewStore([]byte(sm.Cfg.CookieStoreKey))

	// auth
	r.Use(ginsession.Sessions("backbone_session", sm.CookieStore))
	group := r.Group("/auth/sessions")
	group.POST("/login", sm.LoginHandler)
	group.GET("/userinfo", sm.SessionMiddleware(), sm.UserInfoHandler)
	group.GET("/logout", sm.LogoutHandler)
}

func (sm *SessionManager) SessionMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := ginsession.Default(c)
		sessionToken := session.Get("token")
		if sessionToken == nil {
			bberr.MakeError(c, bberr.UserNotLoggedIn)
			c.Abort()
			return
		}
		c.Set("username", session.Get("username"))
		c.Set("roles", session.Get("roles"))
		c.Next()
	}
}

// handlers
func (sm *SessionManager) LoginHandler(c *gin.Context) {
	var userLoginReq LoginRequest
	if err := c.ShouldBindJSON(&userLoginReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := sm.SyncClient.GetUser(userLoginReq.Username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Wrong username or password"})
		return
	}
	passwordMatch := sm.SyncClient.AssertPassword(user.Email, userLoginReq.Password)

	if !passwordMatch {
		c.JSON(http.StatusNotFound, gin.H{"error": "Wrong username or password"})
		return
	}
	sm.createSession(c, userLoginReq.Username)
	c.JSON(http.StatusOK, gin.H{"message": "logged in"})
}

func (sm *SessionManager) LogoutHandler(c *gin.Context) {
	session := ginsession.Default(c)
	opts := ginsession.Options{
		MaxAge:   -1,
		Secure:   false,
		HttpOnly: true,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
	}
	session.Options(opts)
	session.Save()
}

func (sm *SessionManager) UserInfoHandler(c *gin.Context) {
	logger := misc.GetLogger()
	userAuth, err := auth.BuildAuthFromCtx(c)
	if err != nil {
		logger.Warnf("could not get userinfo with err: %s", err.Error())
		bberr.MakeError(c, bberr.CannotExtractSessionInfo)
		return
	}
	c.JSON(http.StatusOK, userAuth)
}

// private
func (sm *SessionManager) createSession(c *gin.Context, username string) {
	logger := misc.GetLogger()
	sessionToken := xid.New().String()
	session := ginsession.Default(c)
	opts := ginsession.Options{
		MaxAge:   int(12 * time.Hour),
		Secure:   false,
		HttpOnly: true,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
	}
	session.Options(opts)
	session.Set("username", username)
	roles, err := sm.SyncClient.GetUserRoles(username)
	if err == nil {
		rolesDto := &k8s.RoleResourceDto{
			Roles: roles,
		}
		roleBytes, _ := json.Marshal(rolesDto)
		session.Set("roles", string(roleBytes))
	}
	session.Set("token", sessionToken)
	err = session.Save()
	if err != nil {
		logger.Warnf("could not save session for user %+v, err %+v", username, err)
	}
}
