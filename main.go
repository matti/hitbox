package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/go-redis/redis/v8"
)

var ttfb int = 0

func hostname() string {
	hostname := os.Getenv("HOSTNAME")
	if hostname == "" {
		hostname = "HOSTNAMEless"
	}

	return hostname
}

func keyFor(parts ...string) string {
	tokens := append([]string{"hitbox"}, parts...)

	var sb strings.Builder
	for _, token := range tokens {
		sb.WriteString(token)
		sb.WriteString(":")
	}

	return sb.String()
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
		hostname := hostname()

		r.Incr(c, keyFor("bg", "inflight", hostname))

		r.ZAdd(c, keyFor("z", "sessions"), &redis.Z{Score: float64(time.Now().Unix()), Member: sessionId})
		r.ZAdd(c, keyFor("z", "ips"), &redis.Z{Score: float64(time.Now().Unix()), Member: ip})
		r.ZAdd(c, keyFor("z", "hits"), &redis.Z{Score: float64(time.Now().Unix()), Member: hit})

		r.PFAdd(c, keyFor("ips"), ip)
		r.PFAdd(c, keyFor("sessions"), sessionId)
		r.PFAdd(c, keyFor("hits"), hit)

		r.Decr(c, keyFor("bg", "inflight", hostname))
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

var keepSeconds = int64(60)

func main() {
	redisURL, ok := os.LookupEnv("REDIS_URL")
	if !ok {
		redisURL = "redis://localhost:6379/0"
	}
	fmt.Println("Using redis from: " + redisURL)

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
		hostname := hostname()
		zRecent := strconv.FormatInt(time.Now().Unix()-keepSeconds, 10)

		ips, _ := redisClient.PFCount(c, keyFor("ips")).Result()
		sessions, _ := redisClient.PFCount(c, keyFor("sessions")).Result()
		hits, _ := redisClient.PFCount(c, keyFor("hits")).Result()

		zIps, _ := redisClient.ZCount(c, keyFor("z", "ips"), zRecent, "+inf").Result()
		zSessions, _ := redisClient.ZCount(c, keyFor("z", "sessions"), zRecent, "+inf").Result()
		zHits, _ := redisClient.ZCount(c, keyFor("z", "hits"), zRecent, "+inf").Result()

		var bgInflight int
		bgInflightString, _ := redisClient.Get(c, keyFor("bg", "inflight", hostname)).Result()
		if bgInflightString == "" {
			bgInflight = -1
		} else {
			bgInflight, _ = strconv.Atoi(bgInflightString)
		}

		c.JSON(http.StatusOK, gin.H{
			"hostname":    hostname,
			"ips":         ips,
			"sessions":    sessions,
			"hits":        hits,
			"bg:inflight": bgInflight,
			"z:ips":       zIps,
			"z:sessions":  zSessions,
			"z:hits":      zHits,
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

	r.GET("/resetz", func(c *gin.Context) {
		redisClient.FlushDB(c)
		c.String(http.StatusOK, "flushed")
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

			recent := strconv.FormatInt(time.Now().Unix()-keepSeconds, 10)
			for _, key := range []string{"ips", "sessions", "hits"} {
				redisClient.ZRemRangeByScore(context.TODO(), keyFor("z", key), "-inf", recent)
			}

			time.Sleep(1 * time.Second)
		}
	}()

	fmt.Println("listening at :8080")
	r.Run(":" + port)
}
