// Package service 承载 user 服务的业务逻辑，同时实现 gRPC 生成的 UserServiceServer 接口。
//
// 设计决策：这一层是唯一知道「proto 消息 <-> 领域模型」怎么互转的地方。
// repository 只认识 model.User，gRPC 客户端只认识 userpb.User，
// 两边的转换集中在这里，不散落在 repository 或 handler 里。
//
// 密码哈希（bcrypt）和 JWT 签发也放在这一层：repository 不该知道"密码"这个
// 业务概念（它只管存取一个叫 password_hash 的字段），gRPC 传输层
// （proto）也不该关心 token 是怎么算出来的。Service 是业务规则唯一的落脚点。
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

// Service 实现 userpb.UserServiceServer。嵌入 UnimplementedUserServiceServer
// 是 protoc-gen-go-grpc 要求的前向兼容写法：以后 proto 里加新 rpc 时，
// 老的 Service 实现不会因为没实现新方法而编译失败。
type Service struct {
	userpb.UnimplementedUserServiceServer

	repo repository.Repository
	log  *zap.Logger

	// jwtSecret/jwtExpireHours 从 pkg/config.JWTConfig 传入，Service 本身
	// 不知道配置是从 yaml 还是环境变量来的——和 pkg/jwt 的设计保持一致。
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

// Login 按用户名查记录 -> bcrypt 比对密码 -> 签发 JWT。
//
// 「用户不存在」和「密码错误」两种失败原因，对外都返回同一句
// "invalid username or password"：如果分开提示（比如"用户不存在"），
// 攻击者可以拿一批用户名批量试探，用响应差异筛出哪些用户名是真实注册过的
// （用户枚举攻击）。真实失败原因只记录在日志里，供内部排查。
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

// GetRandomAddress（阶段 5B）：随机取一条 mock 收货信息。读公开数据池，
// 不验身份——它不是任何用户的私密资源；"要不要登录才能看到"由网关的
// 路由（挂在 auth 组下）决定，service 层不做重复裁决。
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
