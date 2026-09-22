package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type AuthStore struct {
	db *pgxpool.Pool
}

func NewAuthStore(db *pgxpool.Pool) *AuthStore {
	return &AuthStore{db: db}
}

func (s *AuthStore) EnsureBootstrapUser(ctx context.Context, email, password, role string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil
	}

	var existingID int64
	err := s.db.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&existingID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	_, err = s.db.Exec(ctx, `
    INSERT INTO users (email, hashed_password, role)
    VALUES ($1, $2, $3)
    ON CONFLICT (email) DO NOTHING
  `, email, string(hashed), role)
	return err
}

func (s *AuthStore) Authenticate(ctx context.Context, email, password string) (model.User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return model.User{}, fmt.Errorf("请输入邮箱与密码")
	}

	var user model.User
	var hashedPassword string
	err := s.db.QueryRow(ctx, `
    SELECT id, email, role, hashed_password
    FROM users
    WHERE email = $1
  `, email).Scan(&user.ID, &user.Email, &user.Role, &hashedPassword)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.User{}, fmt.Errorf("邮箱或密码不正确，请重新输入")
		}
		return model.User{}, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password)); err != nil {
		return model.User{}, fmt.Errorf("邮箱或密码不正确，请重新输入")
	}

	return user, nil
}

func (s *AuthStore) GetUserByID(ctx context.Context, id int64) (model.User, error) {
	var user model.User
	err := s.db.QueryRow(ctx, `SELECT id, email, role FROM users WHERE id = $1`, id).Scan(&user.ID, &user.Email, &user.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.User{}, fmt.Errorf("未找到该用户，请重新登录")
		}
		return model.User{}, err
	}
	return user, nil
}

// GetUserByEmail 按邮箱读取用户（Issue #160 T02：解析默认工作区的初始管理员）。
//
// 与 Authenticate 的区别：不校验密码。因此它**不能**用于登录路径，
// 只用于「服务端已经知道该用户应当存在」的初始化与治理场景。
// 邮箱统一小写并去空白，与 EnsureBootstrapUser / Authenticate 的归一化一致 ——
// 三处不一致会让「引导建的用户」在后续按邮箱查不到。
func (s *AuthStore) GetUserByEmail(ctx context.Context, email string) (model.User, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return model.User{}, pgx.ErrNoRows
	}

	var user model.User
	err := s.db.QueryRow(ctx, `SELECT id, email, role FROM users WHERE email = $1`, normalized).
		Scan(&user.ID, &user.Email, &user.Role)
	if err != nil {
		return model.User{}, err
	}
	return user, nil
}
