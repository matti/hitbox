package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/go-redis/redis/v8"
)

var ttfb int = 0

func keyFor(what string, id string) string {
	return fmt.Sprintf("hitbox:%s:%s", what, id)
}
func page(c *gin.Context, r *redis.Client, current int) {
	start := time.Now()

	sessionId := ""
	session := sessions.Default(c)
	existingSession := session.Get("id")
	if existingSession == nil {
		sessionId = uuid.New().String()
		session.Set("id", sessionId)
		session.Save()
	} else {
		sessionId = fmt.Sprintf("%v", existingSession)
	}

	hit := uuid.New().String()

	next := current + 1
	ip := c.ClientIP()

	go func() {
		r.Incr(c, keyFor("bg", "inflight"))

		r.Incr(c, keyFor("ip", ip))
		r.Incr(c, keyFor("session", sessionId))
		r.Incr(c, keyFor("hit", hit))

		r.Decr(c, keyFor("bg", "inflight"))
	}()

	overhead := time.Since(start)
	var filler time.Duration
	if ttfb > 0 {
		filler = time.Duration(ttfb)*time.Millisecond - overhead - (3200000 * time.Nanosecond)
		if filler > 0 {
			time.Sleep(filler)
		}
	}

	c.HTML(http.StatusOK, "page.tmpl", gin.H{
		"current":  current,
		"next":     next,
		"ttfb":     ttfb,
		"ip":       ip,
		"session":  sessionId,
		"hit":      hit,
		"overhead": overhead,
		"filler":   filler,
	})
}
func main() {
	redisURL, ok := os.LookupEnv("REDIS_URL")
	if !ok {
		redisURL = "redis://localhost:6379/0"
	}
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		panic(err)
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr:     redisOpts.Addr,
		Password: redisOpts.Password,
	})

	port := "8080"
	if p, ok := os.LookupEnv("PORT"); ok {
		port = p
	}

	r := gin.Default()
	store := cookie.NewStore([]byte("secret"))
	r.Use(sessions.Sessions("hitbox", store))

	r.LoadHTMLGlob("./views/*")

	r.GET("/", func(c *gin.Context) {
		page(c, redisClient, 1)
	})

	r.GET("/page/:current", func(c *gin.Context) {
		current, _ := strconv.Atoi(c.Param("current"))
		page(c, redisClient, current)
	})

	r.GET("/metrics", func(c *gin.Context) {
		ips, _ := redisClient.Keys(c, keyFor("ip", "*")).Result()
		sessions, _ := redisClient.Keys(c, keyFor("session", "*")).Result()
		hits, _ := redisClient.Keys(c, keyFor("hit", "*")).Result()
		bgInflight, _ := redisClient.Get(c, keyFor("bg", "inflight")).Result()

		c.JSON(http.StatusOK, gin.H{
			"ips":         len(ips),
			"sessions":    len(sessions),
			"hits":        len(hits),
			"bg:inflight": bgInflight,
		})
	})

	r.GET("/healthz", func(c *gin.Context) {
		_, err := redisClient.Info(c).Result()
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
		} else {
			c.String(http.StatusOK, "ok")
		}
	})

	r.GET("/set/:key/:value", func(c *gin.Context) {
		key := c.Param("key")
		value, _ := strconv.Atoi(c.Param("value"))

		redisClient.Set(c, keyFor("set", key), value, 0)
		c.String(http.StatusOK, "ok")
	})

	r.GET("/get/:key", func(c *gin.Context) {
		key := c.Param("key")

		value, err := redisClient.Get(c, keyFor("set", key)).Result()

		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
		} else {
			c.String(http.StatusOK, value)
		}
	})

	r.GET("/flush", func(c *gin.Context) {
		result, err := redisClient.FlushAll(c).Result()
		c.String(http.StatusOK, result, err)
	})

	go func() {
		for {
			ttfbString, err := redisClient.Get(context.TODO(), keyFor("set", "ttfb")).Result()
			if err != nil {
				ttfb = 0
			} else {
				ttfb, _ = strconv.Atoi(ttfbString)
			}
			time.Sleep(1 * time.Second)
		}
	}()

	fmt.Println("listening at :8080")
	r.Run(":" + port)
}
