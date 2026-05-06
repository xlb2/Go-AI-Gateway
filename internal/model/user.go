package model

import "time"

//后面的反引号 ` 是 Go 语言特有的 Tag。gorm:"..." 是在给数据库下指令（建主键、设长度、加唯一索引）

type User struct {
	ID       uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	Username string `gorm:"type:varchar(50);uniqueIndex;not null" json:"username"`
	// json:"-" 是在给外网防火墙下指令（绝对不能把密码泄露给前端的 JSON 里）
	Password  string    `gorm:"type:varchar(255);not null" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
