package auth

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/dgunzy/go-book/models"
	"github.com/joho/godotenv"
)

var (
	secretKey       = ""
	tokenExpiration = 120 * time.Hour
)

func init() {
	err := godotenv.Load()
	if err != nil {
		// Handle the error if .env file is not found or cannot be loaded
		// You can choose to use default values or log the error and continue
		fmt.Println(err)
	} else {
		secretKey = os.Getenv("SECRET_KEY")
		if err != nil {
			// Handle the error if TOKEN_EXPIRATION is not a valid integer
			// You can choose to use a default value or log the error and continue
			tokenExpiration = 24 * time.Hour
		} else {
			tokenExpiration = time.Duration(120) * time.Hour
		}
	}
}

func GenerateToken(user *models.User) (string, error) {
	claims := jwt.MapClaims{
		"user_id":  user.UserID,
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(tokenExpiration).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

func ValidateToken(tokenString string) (*jwt.Token, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("invalid signing method")
		}
		return []byte(secretKey), nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, errors.New("invalid token")
	}

	return token, nil
}
