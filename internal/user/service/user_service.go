// Package service 承载 user 服务的业务逻辑，同时实现 gRPC 生成的 UserServiceServer 接口。

package service

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	apperrors "go-ecom-admin/pkg/errors"
	appjwt "go-ecom-admin/pkg/jwt"

	"go-ecom-admin/internal/user/model"
	"go-ecom-admin/internal/user/repository"
	userpb "go-ecom-admin/proto/user"
)

type Service struct {
	userpb.UnimplementedUserServiceServer

	repo repository.Repository
	log  *zap.Logger

	jwtSecret      string
	jwtExpireHours int
}

func New(repo repository.Repository, log *zap.Logger, jwtSecret string, jwtExpireHours int) *Service {
	return &Service{repo: repo, log: log, jwtSecret: jwtSecret, jwtExpireHours: jwtExpireHours}
}

func (s *Service) GetUser(ctx context.Context, req *userpb.GetUserRequest) (*userpb.GetUserResponse, error) {
	u, err := s.repo.GetByID(ctx, req.GetId())
	if err != nil {
		s.log.Warn("get user failed", zap.Uint64("id", req.GetId()), zap.Error(err))
		return nil, apperrors.ToGRPCStatus(err)
	}
	return &userpb.GetUserResponse{User: toProto(u)}, nil
}

// Register 校验入参 -> bcrypt 哈希密码 -> 落库。哈希失败/参数非法都不应该
// 触达数据库，所以校验和哈希都在 repo.Create 之前完成。
func (s *Service) Register(ctx context.Context, req *userpb.RegisterRequest) (*userpb.RegisterResponse, error) {
	if req.GetUsername() == "" || req.GetEmail() == "" {
		err := apperrors.InvalidArgument("username and email are required", nil)
		return nil, apperrors.ToGRPCStatus(err)
	}
	// 6 位只是一个很宽松的下限，防止空密码/单字符密码——真正的密码强度
	// 校验（大小写、特殊字符等）属于产品需求，阶段 2 不做过度设计。
	if len(req.GetPassword()) < 6 {
		err := apperrors.InvalidArgument("password must be at least 6 characters", nil)
		return nil, apperrors.ToGRPCStatus(err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		s.log.Error("hash password failed", zap.Error(err))
		return nil, apperrors.ToGRPCStatus(apperrors.Internal("failed to process password", err))
	}

	u := &model.User{
		Username:     req.GetUsername(),
		Email:        req.GetEmail(),
		PasswordHash: string(hash),
	}
	if err := s.repo.Create(ctx, u); err != nil {
		s.log.Warn("register failed", zap.String("username", req.GetUsername()), zap.Error(err))
		return nil, apperrors.ToGRPCStatus(err)
	}

	return &userpb.RegisterResponse{User: toProto(u)}, nil
}

func (s *Service) Login(ctx context.Context, req *userpb.LoginRequest) (*userpb.LoginResponse, error) {
	const invalidCredentialsMsg = "invalid username or password"

	u, err := s.repo.GetByUsername(ctx, req.GetUsername())
	if err != nil {
		s.log.Warn("login failed: user not found", zap.String("username", req.GetUsername()))
		return nil, apperrors.ToGRPCStatus(apperrors.Unauthorized(invalidCredentialsMsg, err))
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.GetPassword())); err != nil {
		s.log.Warn("login failed: password mismatch", zap.String("username", req.GetUsername()))
		return nil, apperrors.ToGRPCStatus(apperrors.Unauthorized(invalidCredentialsMsg, err))
	}

	token, err := appjwt.Generate(u.ID, u.Username, s.jwtSecret, s.jwtExpireHours)
	if err != nil {
		s.log.Error("generate jwt failed", zap.Error(err))
		return nil, apperrors.ToGRPCStatus(apperrors.Internal("failed to issue token", err))
	}

	return &userpb.LoginResponse{Token: token, User: toProto(u)}, nil
}

func toProto(u *model.User) *userpb.User {
	return &userpb.User{
		Id:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		CreatedAt: u.CreatedAt.Unix(),
	}
}

func (s *Service) GetRandomAddress(ctx context.Context, _ *userpb.GetRandomAddressRequest) (*userpb.GetRandomAddressResponse, error) {
	a, err := s.repo.GetRandomAddress(ctx)
	if err != nil {
		s.log.Warn("get random address failed", zap.Error(err))
		return nil, apperrors.ToGRPCStatus(err)
	}
	return &userpb.GetRandomAddressResponse{Address: &userpb.Address{
		Id:           a.ID,
		ReceiverName: a.ReceiverName,
		Phone:        a.Phone,
		Address:      a.Address,
	}}, nil
}
