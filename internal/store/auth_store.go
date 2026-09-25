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

// ErrWeakPassword 表示初始密码不满足最低强度要求。
//
// 用哨兵错误而不是裸字符串比较：handler 需要据此返回**字段级** 422，
// 而字符串匹配会在文案调整时静默失效。
var ErrWeakPassword = errors.New("密码强度不足")

// UserExistsError 表示邮箱已被占用（创建用户时）。
var UserExistsError = errors.New("该邮箱已存在")

// normalizingEmail 统一邮箱归一化（去空白 + 小写）。
//
// 提取成函数是因为「引导建的用户」必须能被「按邮箱创建的用户」查到 ——
// EnsureBootstrapUser / Authenticate / GetUserByEmail / CreateUser 四处
// 只要有一处不归一化，就会出现「用户存在但登录不了」。
func normalizingEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateInitialPassword 校验初始密码强度（issue #197 第 15 条）。
//
// 规则刻意简单且可解释：至少 8 个字符，且**不能与邮箱相同**。
// 为什么不做「必须含大小写+数字+符号」：本系统是内网交付系统，
// 过强的规则会让管理员把密码写在便签上，反而降低实际安全性；
// 而「密码等于邮箱」是最常见的敷衍输入，必须拦住。
func ValidateInitialPassword(email, password string) error {
	if len([]rune(password)) < 8 {
		return ErrWeakPassword
	}
	if strings.EqualFold(strings.TrimSpace(password), normalizingEmail(email)) {
		return ErrWeakPassword
	}
	return nil
}

// CreateUser 直接创建账号（用户名 + 密码，issue #197 第 15 条）。
//
// 为什么不是「邮件邀请」：本部署没有出站邮件，邀请链接永远发不出去，
// 于是「按邮箱添加已有账号」对甲方就是不可用的功能。这里按甲方的
// 明确要求改为「管理员直接设初始密码」。
//
// 返回 model.User（不含 hashed_password）而不是完整行：密码哈希
// 绝不允许离开 store 层。
func (s *AuthStore) CreateUser(ctx context.Context, email, password, role string) (model.User, error) {
	normalized := normalizingEmail(email)
	if normalized == "" {
		return model.User{}, model.FieldErrors{{Field: "email", Message: "必填"}}
	}
	if !strings.Contains(normalized, "@") || strings.HasPrefix(normalized, "@") || strings.HasSuffix(normalized, "@") {
		return model.User{}, model.FieldErrors{{Field: "email", Message: "必须是合法邮箱（登录用户名）"}}
	}
	if role != "admin" && role != "user" {
		return model.User{}, model.FieldErrors{{Field: "role", Message: "只能是 admin 或 user"}}
	}
	if err := ValidateInitialPassword(normalized, password); err != nil {
		return model.User{}, model.FieldErrors{{Field: "password",
			Message: "初始密码至少 8 位，且不能与邮箱相同"}}
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return model.User{}, err
	}

	var user model.User
	err = s.db.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role)
    VALUES ($1, $2, $3)
    ON CONFLICT (email) DO NOTHING
    RETURNING id, email, role`, normalized, string(hashed), role).
		Scan(&user.ID, &user.Email, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING + RETURNING 在冲突时返回 0 行 ——
		// 这正是「邮箱已存在」，而不是「服务端故障」。
		return model.User{}, model.FieldErrors{{Field: "email",
			Message: "该邮箱已经有账号了；要调整权限请直接在成员列表里修改角色"}}
	}
	if err != nil {
		return model.User{}, err
	}
	return user, nil
}
