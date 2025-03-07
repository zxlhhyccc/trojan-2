package web

import (
	"errors"
	"fmt"
	"github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/muesli/cache2go"
	"time"
	"trojan/core"
	"trojan/web/controller"
)

var (
	identityKey    = "id"
	authMiddleware *jwt.GinJWTMiddleware
	err            error
	cache          *cache2go.CacheTable
)

// Login auth用户验证结构体
type Login struct {
	Username string `form:"username" json:"username" binding:"required"`
	Password string `form:"password" json:"password" binding:"required"`
}

type failInfo struct {
	ip    string
	count int
}

func getRealIP(c *gin.Context) string {
	ip := c.ClientIP()
	if ip == "::1" || ip == "127.0.0.1" {
		if len(c.GetHeader("X-Real-IP")) > 0 {
			ip = c.GetHeader("X-Real-IP")
		}
	}
	return ip
}

func jwtInit(timeout int) {
	if cache == nil {
		cache = cache2go.Cache("passError")
	}
	authMiddleware, err = jwt.New(&jwt.GinJWTMiddleware{
		Realm:       "k8s-manager",
		Key:         []byte("secret key"),
		Timeout:     time.Minute * time.Duration(timeout),
		MaxRefresh:  time.Minute * time.Duration(timeout),
		IdentityKey: identityKey,
		SendCookie:  true,
		PayloadFunc: func(data interface{}) jwt.MapClaims {
			if v, ok := data.(*Login); ok {
				return jwt.MapClaims{
					identityKey: v.Username,
				}
			}
			return jwt.MapClaims{}
		},
		IdentityHandler: func(c *gin.Context) interface{} {
			claims := jwt.ExtractClaims(c)
			return &Login{
				Username: claims[identityKey].(string),
			}
		},
		Authenticator: func(c *gin.Context) (interface{}, error) {
			var (
				password  string
				loginVals Login
			)
			if err := c.ShouldBind(&loginVals); err != nil {
				return "", jwt.ErrMissingLoginValues
			}
			userID := loginVals.Username
			pass := loginVals.Password
			if err != nil {
				return nil, err
			}
			if userID != "admin" {
				mysql := core.GetMysql()
				user := mysql.GetUserByName(userID)
				if user == nil {
					return nil, jwt.ErrFailedAuthentication
				}
				password = user.EncryptPass
			} else {
				if password, err = core.GetValue(userID + "_pass"); err != nil {
					return nil, err
				}
			}
			clientIP := getRealIP(c)
			if !cache.Exists(clientIP) {
				if password == pass {
					return &loginVals, nil
				} else {
					cache.Add(clientIP, 30*time.Minute, &failInfo{
						clientIP,
						1,
					})
					return nil, errors.New("已输错1次密码, 还有2次机会")
				}
			} else {
				res, _ := cache.Value(clientIP)
				failCount := res.Data().(*failInfo).count
				if failCount >= 3 {
					return nil, errors.New("已输错3次密码, 请等待30min后解锁")
				} else {
					if password == pass {
						cache.Delete(clientIP)
						return &loginVals, nil
					} else {
						failCount += 1
						cache.Add(clientIP, 30*time.Minute, &failInfo{
							clientIP,
							failCount,
						})
						return nil, errors.New(fmt.Sprintf("已输错%d次密码, 还有%d次机会", failCount, 3-failCount))
					}
				}
			}
		},
		Authorizator: func(data interface{}, c *gin.Context) bool {
			if _, ok := data.(*Login); ok {
				return true
			}
			return false
		},
		Unauthorized: func(c *gin.Context, code int, message string) {
			c.JSON(code, gin.H{
				"code":    code,
				"message": message,
			})
		},
		TokenLookup:   "header: Authorization, query: token, cookie: jwt",
		TokenHeadName: "Bearer",
		TimeFunc:      time.Now,
	})

	if err != nil {
		fmt.Println("JWT Error:" + err.Error())
	}
}

func updateUser(c *gin.Context) {
	responseBody := controller.ResponseBody{Msg: "success"}
	defer controller.TimeCost(time.Now(), &responseBody)
	username := c.DefaultPostForm("username", "admin")
	pass := c.PostForm("password")
	err := core.SetValue(fmt.Sprintf("%s_pass", username), pass)
	if err != nil {
		responseBody.Msg = err.Error()
	}
	c.JSON(200, responseBody)
}

// RequestUsername 获取请求接口的用户名
func RequestUsername(c *gin.Context) string {
	claims := jwt.ExtractClaims(c)
	return claims[identityKey].(string)
}

// Auth 权限router
func Auth(r *gin.Engine, timeout int) *jwt.GinJWTMiddleware {
	jwtInit(timeout)

	newInstall := gin.H{"code": 201, "message": "No administrator account found inside the database", "data": nil}
	r.NoRoute(authMiddleware.MiddlewareFunc(), func(c *gin.Context) {
		claims := jwt.ExtractClaims(c)
		fmt.Printf("NoRoute claims: %#v\n", claims)
		c.JSON(404, gin.H{"code": 404, "message": "Page not found"})
	})
	r.GET("/auth/check", func(c *gin.Context) {
		result, _ := core.GetValue("admin_pass")
		if result == "" {
			c.JSON(201, newInstall)
		} else {
			title, err := core.GetValue("login_title")
			if err != nil {
				title = "trojan 管理平台"
			}
			c.JSON(200, gin.H{
				"code":    200,
				"message": "success",
				"data": map[string]string{
					"title": title,
				},
			})
		}
	})
	r.POST("/auth/login", authMiddleware.LoginHandler)
	r.POST("/auth/register", updateUser)
	authO := r.Group("/auth")
	authO.Use(authMiddleware.MiddlewareFunc())
	{
		authO.GET("/loginUser", func(c *gin.Context) {
			result, _ := core.GetValue("admin_pass")
			if result == "" {
				c.JSON(201, newInstall)
			} else {
				c.JSON(200, gin.H{
					"code":    200,
					"message": "success",
					"data": map[string]string{
						"username": RequestUsername(c),
					},
				})
			}
		})
		authO.POST("/reset_pass", updateUser)
		authO.POST("/logout", authMiddleware.LogoutHandler)
		authO.POST("/refresh_token", authMiddleware.RefreshHandler)
	}
	return authMiddleware
}
