package model

type User struct {
  ID    int64  `json:"id"`
  Email string `json:"email"`
  Role  string `json:"role"`
}

type LoginRequest struct {
  Email    string `json:"email"`
  Password string `json:"password"`
}

type AuthResponse struct {
  User User `json:"user"`
}
