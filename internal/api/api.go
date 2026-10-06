package api

import (
	"errors"
	"log"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/logger"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"workshop/s2/internal/models"
)

func New(db *gorm.DB) *fiber.App {
	app := fiber.New(fiber.Config{
		Immutable: true,
		BodyLimit: 16 * 1024,
		ErrorHandler: func(c fiber.Ctx, err error) error {
			code, message := fiber.StatusInternalServerError, "internal server error"
			var httpErr *fiber.Error
			if errors.As(err, &httpErr) {
				code, message = httpErr.Code, httpErr.Message
			}
			if code >= 500 {
				log.Printf("request failed: %v", err)
			}
			return c.Status(code).JSON(fiber.Map{"error": message})
		},
	})
	app.Use(logger.New(), recover.New())
	app.Get("/health", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	app.Post("/users", func(c fiber.Ctx) error {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := c.Bind().JSON(&input); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid JSON body")
		}
		input.Username = strings.TrimSpace(input.Username)
		if n := utf8.RuneCountInString(input.Username); n < 1 || n > 64 {
			return fiber.NewError(fiber.StatusBadRequest, "username must be 1 to 64 characters")
		}
		if n := len(input.Password); n < 8 || n > 72 {
			return fiber.NewError(fiber.StatusBadRequest, "password must be 8 to 72 bytes")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		user := models.User{Username: input.Username, Password: string(hash)}
		if err := db.Create(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return fiber.NewError(fiber.StatusConflict, "username already exists")
			}
			return err
		}
		c.Set("Location", "/users/"+strconv.FormatUint(uint64(user.ID), 10))
		return c.Status(fiber.StatusCreated).JSON(user)
	})
	app.Get("/users", func(c fiber.Ctx) error {
		users := make([]models.User, 0)
		if err := db.Select("id", "username").Order("id ASC").Find(&users).Error; err != nil {
			return err
		}
		return c.JSON(users)
	})
	app.Get("/users/:id", func(c fiber.Ctx) error {
		id, err := strconv.ParseUint(c.Params("id"), 10, strconv.IntSize)
		if err != nil || id == 0 {
			return fiber.NewError(fiber.StatusBadRequest, "invalid user ID")
		}
		var user models.User
		if err := db.Select("id", "username").First(&user, uint(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fiber.NewError(fiber.StatusNotFound, "user not found")
			}
			return err
		}
		return c.JSON(user)
	})
	return app
}
