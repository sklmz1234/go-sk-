// user 服务的 service 层单元测试。

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperrors "go-ecom-admin/pkg/errors"
	appjwt "go-ecom-admin/pkg/jwt"

	"go-ecom-admin/internal/user/model"
	"go-ecom-admin/internal/user/repository/mocks"
	userpb "go-ecom-admin/proto/user"
)

const (
	testJWTSecret = "unit-test-secret"
	testUsername  = "sklmz"
	testEmail     = "sklmz@example.com"
	testPassword  = "right-pass"
)

func newTestService(t *testing.T) (*Service, *mocks.MockRepository) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	return New(repo, zaptest.NewLogger(t), testJWTSecret, 1), repo
}

func storedUser(t *testing.T) *model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	require.NoError(t, err)
	return &model.User{ID: 42, Username: testUsername, Email: testEmail, PasswordHash: string(hash)}
}

func TestRegister_Validation(t *testing.T) {
	tests := []struct {
		name string
		req  *userpb.RegisterRequest
	}{
		{"缺少用户名", &userpb.RegisterRequest{Username: "", Email: testEmail, Password: testPassword}},
		{"缺少邮箱", &userpb.RegisterRequest{Username: testUsername, Email: "", Password: testPassword}},
		{"密码太短", &userpb.RegisterRequest{Username: testUsername, Email: testEmail, Password: "12345"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestService(t) // mock 零期望：任何 repo 调用都会失败

			resp, err := svc.Register(context.Background(), tt.req)

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestRegister_Success(t *testing.T) {
	svc, repo := newTestService(t)

	var captured *model.User
	repo.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, u *model.User) {
			captured = u
			u.ID = 42 // 模拟 GORM 的行为：Create 后回填自增主键
		}).
		Return(nil)

	resp, err := svc.Register(context.Background(), &userpb.RegisterRequest{
		Username: testUsername, Email: testEmail, Password: testPassword,
	})

	require.NoError(t, err)
	require.NotNil(t, resp.GetUser())

	assert.NotEqual(t, testPassword, captured.PasswordHash)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(captured.PasswordHash), []byte(testPassword)))

	assert.Equal(t, uint64(42), resp.GetUser().GetId())
	assert.Equal(t, testUsername, resp.GetUser().GetUsername())
	assert.Equal(t, testEmail, resp.GetUser().GetEmail())
}

func TestRegister_DuplicateUsername(t *testing.T) {
	svc, repo := newTestService(t)
	repo.EXPECT().Create(mock.Anything, mock.Anything).
		Return(apperrors.AlreadyExists("username or email already exists", nil))

	resp, err := svc.Register(context.Background(), &userpb.RegisterRequest{
		Username: testUsername, Email: testEmail, Password: testPassword,
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
}

func TestRegister_RepoInternalError(t *testing.T) {
	svc, repo := newTestService(t)
	repo.EXPECT().Create(mock.Anything, mock.Anything).
		Return(apperrors.Internal("failed to create user", nil))

	resp, err := svc.Register(context.Background(), &userpb.RegisterRequest{
		Username: testUsername, Email: testEmail, Password: testPassword,
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, codes.Internal, status.Code(err))
}

func TestLogin_AntiEnumeration(t *testing.T) {
	tests := []struct {
		name      string
		username  string
		password  string
		setupRepo func(repo *mocks.MockRepository)
	}{
		{
			name:     "用户不存在",
			username: "nobody",
			password: testPassword,
			setupRepo: func(repo *mocks.MockRepository) {
				repo.EXPECT().GetByUsername(mock.Anything, "nobody").
					Return(nil, apperrors.NotFound("user not found", nil))
			},
		},
		{
			name:     "密码错误",
			username: testUsername,
			password: "wrong-pass",
			setupRepo: func(repo *mocks.MockRepository) {
				repo.EXPECT().GetByUsername(mock.Anything, testUsername).
					Return(storedUser(t), nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService(t)
			tt.setupRepo(repo)

			_, err := svc.Login(context.Background(), &userpb.LoginRequest{
				Username: tt.username, Password: tt.password,
			})

			require.Error(t, err)
			assert.Equal(t, codes.Unauthenticated, status.Code(err))
			assert.Equal(t, "invalid username or password", status.Convert(err).Message())
		})
	}
}

func TestLogin_Success(t *testing.T) {
	svc, repo := newTestService(t)
	repo.EXPECT().GetByUsername(mock.Anything, testUsername).
		Return(storedUser(t), nil)

	resp, err := svc.Login(context.Background(), &userpb.LoginRequest{
		Username: testUsername, Password: testPassword,
	})

	require.NoError(t, err)
	require.NotEmpty(t, resp.GetToken())

	claims, err := appjwt.Parse(resp.GetToken(), testJWTSecret)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), claims.UserID)
	assert.Equal(t, testUsername, claims.Username)

	assert.Equal(t, testUsername, resp.GetUser().GetUsername())
}

func TestGetUser_NotFound(t *testing.T) {
	svc, repo := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(999)).
		Return(nil, apperrors.NotFound("user not found", nil))

	resp, err := svc.GetUser(context.Background(), &userpb.GetUserRequest{Id: 999})

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetUser_Success(t *testing.T) {
	svc, repo := newTestService(t)
	repo.EXPECT().GetByID(mock.Anything, uint64(42)).
		Return(storedUser(t), nil)

	resp, err := svc.GetUser(context.Background(), &userpb.GetUserRequest{Id: 42})

	require.NoError(t, err)
	assert.Equal(t, uint64(42), resp.GetUser().GetId())
	assert.Equal(t, testEmail, resp.GetUser().GetEmail())
}
