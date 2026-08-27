package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/biox-dev/gobrave/internal/types"
)

type mockUserRepo struct {
	userByEmail *types.User
	userByID    *types.User
}

func (m *mockUserRepo) CreateUser(ctx context.Context, user *types.User) error { return nil }
func (m *mockUserRepo) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	if m.userByID != nil && m.userByID.ID == id {
		return m.userByID, nil
	}
	return nil, errors.New("user not found")
}
func (m *mockUserRepo) GetUserByEmail(ctx context.Context, email string) (*types.User, error) {
	if m.userByEmail != nil && m.userByEmail.Email == email {
		return m.userByEmail, nil
	}
	return nil, errors.New("user not found")
}
func (m *mockUserRepo) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	return nil, errors.New("not implemented")
}
func (m *mockUserRepo) GetUserByTenantID(ctx context.Context, tenantID uint64) (*types.User, error) {
	return nil, errors.New("not implemented")
}
func (m *mockUserRepo) UpdateUser(ctx context.Context, user *types.User) error { return nil }
func (m *mockUserRepo) DeleteUser(ctx context.Context, id string) error        { return nil }
func (m *mockUserRepo) ListUsers(ctx context.Context, offset, limit int) ([]*types.User, error) {
	return []*types.User{}, nil
}
func (m *mockUserRepo) SearchUsers(ctx context.Context, query string, limit int) ([]*types.User, error) {
	return []*types.User{}, nil
}

type mockTokenRepo struct {
	storeOnCreate bool
	tokens        map[string]*types.AuthToken
}

func (m *mockTokenRepo) CreateToken(ctx context.Context, token *types.AuthToken) error {
	if m.storeOnCreate {
		if m.tokens == nil {
			m.tokens = map[string]*types.AuthToken{}
		}
		m.tokens[token.Token] = token
	}
	return nil
}

func (m *mockTokenRepo) GetTokenByValue(ctx context.Context, tokenValue string) (*types.AuthToken, error) {
	if m.tokens == nil {
		return nil, errors.New("token not found")
	}
	token, ok := m.tokens[tokenValue]
	if !ok {
		return nil, errors.New("token not found")
	}
	return token, nil
}

func (m *mockTokenRepo) GetTokensByUserID(ctx context.Context, userID string) ([]*types.AuthToken, error) {
	var out []*types.AuthToken
	for _, token := range m.tokens {
		if token.UserID == userID {
			out = append(out, token)
		}
	}
	return out, nil
}

func (m *mockTokenRepo) UpdateToken(ctx context.Context, token *types.AuthToken) error {
	if m.tokens == nil {
		m.tokens = map[string]*types.AuthToken{}
	}
	m.tokens[token.Token] = token
	return nil
}

func (m *mockTokenRepo) DeleteToken(ctx context.Context, id string) error { return nil }
func (m *mockTokenRepo) DeleteExpiredTokens(ctx context.Context) error    { return nil }
func (m *mockTokenRepo) RevokeTokensByUserID(ctx context.Context, userID string) error {
	for _, token := range m.tokens {
		if token.UserID == userID {
			token.IsRevoked = true
		}
	}
	return nil
}

func resetJWTSecretForTest(t *testing.T) {
	t.Helper()
	jwtSecret = ""
	jwtSecretOnce = sync.Once{}
}

func newTestUser(t *testing.T) *types.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	now := time.Now()
	return &types.User{
		ID:           "u-1",
		Username:     "tester",
		Email:        "tester@example.com",
		PasswordHash: string(hash),
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func TestLoginTokenValidate_FailsWhenTokenRecordMissing(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-1")
	resetJWTSecretForTest(t)

	user := newTestUser(t)
	service := &userService{
		userRepo: &mockUserRepo{
			userByEmail: user,
			userByID:    user,
		},
		// Simulate storage layer inconsistency: create succeeds but token is not queryable.
		tokenRepo: &mockTokenRepo{storeOnCreate: false},
	}

	loginResp, err := service.Login(context.Background(), &types.LoginRequest{
		Email:    user.Email,
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if !loginResp.Success || loginResp.Token == "" {
		t.Fatalf("expected login token, got %+v", loginResp)
	}

	validatedUser, err := service.ValidateToken(context.Background(), loginResp.Token)
	if err == nil {
		t.Fatalf("expected validation error, got user %+v", validatedUser)
	}
	if err.Error() != "token is revoked" {
		t.Fatalf("expected token is revoked, got %v", err)
	}
}

func TestLoginTokenValidate_FailsWhenJWTSecretChanges(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-2")
	resetJWTSecretForTest(t)

	user := newTestUser(t)
	tokenRepo := &mockTokenRepo{storeOnCreate: true, tokens: map[string]*types.AuthToken{}}
	service := &userService{
		userRepo: &mockUserRepo{
			userByEmail: user,
			userByID:    user,
		},
		tokenRepo: tokenRepo,
	}

	loginResp, err := service.Login(context.Background(), &types.LoginRequest{
		Email:    user.Email,
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if !loginResp.Success || loginResp.Token == "" {
		t.Fatalf("expected login token, got %+v", loginResp)
	}

	t.Setenv("JWT_SECRET", "another-secret-after-login")
	resetJWTSecretForTest(t)

	validatedUser, err := service.ValidateToken(context.Background(), loginResp.Token)
	if err == nil {
		t.Fatalf("expected validation error, got user %+v", validatedUser)
	}
	if err.Error() != "invalid token" {
		t.Fatalf("expected invalid token, got %v", err)
	}
}
