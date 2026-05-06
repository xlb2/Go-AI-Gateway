package handler

import (
	"context"
	"errors"
	"fmt"
	"go_im_gateway/internal/model"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var jwtSecret = []byte("go_im_gateway_super_secret_key_2026")

type CustomClaims struct {
	UserID uint `json:"user_id"`
	jwt.RegisteredClaims
}

type UserHandler struct {
	DB *gorm.DB
}

type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=20"`
	Password string `json:"password" binding:"required,min=6"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (h *UserHandler) Login(c *gin.Context) {
	var req LoginRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "参数格式不合格"})
		return
	}

	var dbUser model.User
	if err := h.DB.Where("username = ?", req.Username).First(&dbUser).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或者密码错误"})
		return
	}

	err := bcrypt.CompareHashAndPassword([]byte(dbUser.Password), []byte(req.Password))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或者密码错误"})
		return
	}
	claims := CustomClaims{
		UserID: dbUser.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			//物理防线，设置护照有效期为24小时
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			//记录签发的时间
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString(jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "护照签发失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "登陆成功",
		"token":   tokenString,
	})
}

func (h *UserHandler) Register(c *gin.Context) {
	var req RegisterRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数安检没过:" + err.Error()})
		return
	}

	ctx, cannel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cannel()
	var existsUser model.User
	err := h.DB.WithContext(ctx).Where("username = ?", req.Username).First(&existsUser).Error
	if err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "该护照名已被霸占，请换一个"})
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "后端数据库物理断联，服务临时降级，请稍后重试"})
		return
	}

	hashPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统底层加密已经崩溃"})
		return
	}

	newUser := model.User{
		Username: req.Username,
		Password: string(hashPassword),
	}
	err = h.DB.WithContext(ctx).Create(&newUser).Error
	if err != nil {
		fmt.Printf(" [物理警报] 数据库写入触发异常: %v\n", err)
		if strings.Contains(err.Error(), "Duplicate entry") {
			c.JSON(http.StatusConflict, gin.H{"error": "该护照名已被霸占，请换一个"})
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "底层基础设施响应超时，系统已触发 Fail-Fast 熔断保护！"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "后端数据库物理断联，服务临时降级，请稍后重试"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "完美接客!安检通过。",
		"分配到的ID":  newUser.ID,
		"注册账号":    newUser.Username,
	})
}
