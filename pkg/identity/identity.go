// Package identity 统一处理"调用方是谁"在 gRPC 调用链上的传递。

package identity

import (
	"context"
	"strconv"

	"google.golang.org/grpc/metadata"

	apperrors "go-ecom-admin/pkg/errors"
)

// MetadataKeyUserID 是 user_id 在 gRPC metadata 里的 key（小写下划线风格，
// gRPC metadata key 不区分大小写但统一规范成这种形式）。
const MetadataKeyUserID = "user_id"

const SystemUserID = 0

func InjectOutgoing(ctx context.Context, userID uint64) context.Context {
	return metadata.AppendToOutgoingContext(ctx, MetadataKeyUserID, strconv.FormatUint(userID, 10))
}

func FromIncoming(ctx context.Context) (uint64, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return 0, apperrors.Unauthorized("missing caller identity", nil)
	}

	vals := md.Get(MetadataKeyUserID)
	if len(vals) == 0 {
		return 0, apperrors.Unauthorized("missing caller identity", nil)
	}

	uid, err := strconv.ParseUint(vals[0], 10, 64)
	if err != nil || uid == SystemUserID {
		return 0, apperrors.Unauthorized("invalid caller identity", err)
	}
	return uid, nil
}

func InjectOutgoingSystem(ctx context.Context) context.Context {
	return InjectOutgoing(ctx, SystemUserID)
}

func FromIncomingAllowSystem(ctx context.Context) (uint64, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return 0, apperrors.Unauthorized("missing caller identity", nil)
	}

	vals := md.Get(MetadataKeyUserID)
	if len(vals) == 0 {
		return 0, apperrors.Unauthorized("missing caller identity", nil)
	}

	uid, err := strconv.ParseUint(vals[0], 10, 64)
	if err != nil {
		return 0, apperrors.Unauthorized("invalid caller identity", err)
	}
	return uid, nil
}
