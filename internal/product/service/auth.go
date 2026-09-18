// Package service 的身份读取已收拢到 pkg/identity

package service

import (
	"context"

	"go-ecom-admin/pkg/identity"
)

// userIDFromContext 是 identity.FromIncoming 的包内别名：保留原函数名，
// 本包内所有调用点（Create/Update/Delete/DeductStock/RestoreStock）零改动。
func userIDFromContext(ctx context.Context) (uint64, error) {
	return identity.FromIncoming(ctx)
}
