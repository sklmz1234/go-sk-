// Package database 提供数据库相关的横切能力。
package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// migrateLockName 是全局唯一的锁名：user-service / product-service / seed
// 三个入口共用同一把锁，保证任何时刻整个系统只有一个 AutoMigrate 在跑。
const migrateLockName = "go_ecom_admin_schema_migrate"

func Migrate(db *gorm.DB, wait time.Duration, models ...any) error {
	var got int

	if err := db.Raw("SELECT GET_LOCK(?, ?)", migrateLockName, int(wait.Seconds())).Scan(&got).Error; err != nil {
		return fmt.Errorf("acquire migrate lock: %w", err)
	}
	if got != 1 {
		return fmt.Errorf("acquire migrate lock: timed out after %s (another instance is migrating?)", wait)
	}
	defer func() {

		_ = db.Exec("SELECT RELEASE_LOCK(?)", migrateLockName).Error
	}()

	if err := db.AutoMigrate(models...); err != nil {
		return fmt.Errorf("auto migrate: %w", err)
	}
	return nil
}
