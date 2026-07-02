package bootstrap

import (
	"fmt"
	"go_im_gateway/internal/model"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// InitDB 初始化MySQL连接，带重试机制，自动同步表结构
func InitDB(dsn string) *gorm.DB {
	var db *gorm.DB
	var err error

	for i := 0; i < 10; i++ {
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		if err == nil {
			fmt.Printf(" 数据库物理连接成功！(重试次数: %d)\n", i)
			break
		}
		fmt.Printf(" 无法连接数据库，正在准备第 %d 次重试: %v\n", i, err)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		log.Fatalf("无法连接到数据库：%v ", err)
	}

	// 自动迁移表结构
	err = db.AutoMigrate(&model.User{}, &model.Message{})
	if err != nil {
		panic("数据库迁移失败: " + err.Error())
	}
	fmt.Println("数据库物理表结构已同步！")

	return db
}
