package gosaic

import (
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

type Seed struct {
	Seed        *multipart.FileHeader `form:"seed" binding:"required" json:"seed"`
	Tilesize    int                   `form:"tilesize" binding:"required" json:"tilesize"`
	Comparesize int                   `form:"comparesize" binding:"required" json:"comparesize"`
	RedisLabel  string                `form:"redislabel" binding:"required" json:"redislabel"`
	OutputSize  int                   `form:"outputsize" binding:"required" json:"outputsize"`
	CompareDist float64               `form:"comparedist" binding:"required" json:"comparedist"`
	Unique      bool                  `form:"unique" binding:"-" json:"unique"`
	SmartCrop   bool                  `form:"smartcrop" binding:"-" json:"smartcrop"`
	Progress    bool                  `form:"progress" binding:"-" json:"progress"`
	Workers     int                   `form:"workers" binding:"-" json:"workers"`
}

type Server struct {
	addr      string
	router    *gin.Engine
	redisAddr string
}

func (s *Server) Run() error {
	return s.router.Run(s.addr)
}

func NewServer(addr, redisAddr string) (*Server, error) {
	srv := &Server{
		addr:      addr,
		redisAddr: redisAddr,
	}

	srv.router = gin.Default()
	srv.router.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	srv.router.POST("/seed", func(c *gin.Context) {
		s := Seed{}
		err := c.ShouldBind(&s)
		if err != nil {
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}

		mpf, err := s.Seed.Open()
		if err != nil {
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}

		tmpfile, err := ioutil.TempFile("", "seed.*.jpg")
		if err != nil {
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}

		defer os.Remove(tmpfile.Name()) // clean up

		if _, err := io.Copy(tmpfile, mpf); err != nil {
			tmpfile.Close()
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}
		if err := tmpfile.Close(); err != nil {
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}

		mosaicUUID := uuid.NewString()
		outFile := fmt.Sprintf("mosaics/%s.jpg", mosaicUUID)

		config := Config{
			SeedImage:    tmpfile.Name(),
			TileSize:     s.Tilesize,
			OutputSize:   s.OutputSize,
			OutputImage:  outFile,
			CompareSize:  s.Comparesize,
			CompareDist:  float64(s.CompareDist),
			Unique:       s.Unique,
			SmartCrop:    s.SmartCrop,
			ProgressBar:  false,
			RedisAddr:    srv.redisAddr,
			RedisLabel:   s.RedisLabel,
			HTTPAddr:     addr,
			ProgressText: s.Progress,
			Workers:      s.Workers,
		}

		g, err := New(config)
		if err != nil {
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}

		err = g.Build()
		if err != nil {
			log.Fatal(err)
		}

		stat, err := os.Stat(outFile)
		if err != nil {
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}

		fh, err := os.Open(outFile)
		if err != nil {
			log.Error(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err})
			return
		}
		defer fh.Close()

		c.DataFromReader(http.StatusOK, stat.Size(), "image/jpeg", fh, map[string]string{"Content-Displsition": fmt.Sprintf("attachment; filename=\"%s.jpg\"", mosaicUUID)})
	})

	return srv, nil
}
