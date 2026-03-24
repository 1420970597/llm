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
    return model.User{}, fmt.Errorf("email and password are required")
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
      return model.User{}, fmt.Errorf("invalid email or password")
    }
    return model.User{}, err
  }

  if err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password)); err != nil {
    return model.User{}, fmt.Errorf("invalid email or password")
  }

  return user, nil
}

func (s *AuthStore) GetUserByID(ctx context.Context, id int64) (model.User, error) {
  var user model.User
  err := s.db.QueryRow(ctx, `SELECT id, email, role FROM users WHERE id = $1`, id).Scan(&user.ID, &user.Email, &user.Role)
  if err != nil {
    if errors.Is(err, pgx.ErrNoRows) {
      return model.User{}, fmt.Errorf("user not found")
    }
    return model.User{}, err
  }
  return user, nil
}
