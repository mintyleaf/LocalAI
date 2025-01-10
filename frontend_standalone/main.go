package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/mudler/LocalAI/internal"
	"github.com/mudler/LocalAI/pkg/utils"

	"github.com/mudler/LocalAI/core/config"
	"github.com/mudler/LocalAI/core/gallery"
	laihttp "github.com/mudler/LocalAI/core/http"
	"github.com/mudler/LocalAI/core/http/endpoints/openai"
	"github.com/mudler/LocalAI/core/http/middleware"
	laihttputils "github.com/mudler/LocalAI/core/http/utils"
	"github.com/mudler/LocalAI/core/p2p"

	"github.com/mudler/LocalAI/core/schema"

	"github.com/gofiber/contrib/fiberzerolog"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/favicon"
	"github.com/gofiber/fiber/v2/middleware/filesystem"
	"github.com/gofiber/fiber/v2/middleware/recover"

	// swagger handler

	"github.com/rs/zerolog/log"
)

// @title LocalAI API
// @version 2.0.0
// @description The LocalAI Rest API.
// @termsOfService
// @contact.name LocalAI
// @contact.url https://localai.io
// @license.name MIT
// @license.url https://raw.githubusercontent.com/mudler/LocalAI/master/LICENSE
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization

func main() {
	appHTTP, err := API(&config.ApplicationConfig{})
	if err != nil {
		log.Error().Err(err).Msg("error during HTTP App construction")
		return
	}

	if err := appHTTP.Listen(":8081"); err != nil {
		log.Error().Err(err).Msg("error during HTTP App construction")
		return
	}

}

func makeModelsRequest(url, bearerKey string) (schema.ModelsDataResponse, error) {
	req, err := http.NewRequest("GET", url, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerKey)
	modelsResponse := schema.ModelsDataResponse{}

	if err != nil {
		return modelsResponse, err
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return modelsResponse, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return modelsResponse, err
	}
	err = json.Unmarshal(body, &modelsResponse)
	if err != nil {
		return modelsResponse, err
	}
	return modelsResponse, nil
}

func getModelsConfigs(url, bearerKey string) ([]config.BackendConfig, error) {
	modelsResponse, err := makeModelsRequest(url, bearerKey)
	if err != nil {
		return nil, err
	}
	response := []config.BackendConfig{}

	for _, v := range modelsResponse.Data {
		response = append(response, config.BackendConfig{
			Name: v.ID,
		})
	}
	return response, nil
}

func getModels(url, bearerKey string) ([]string, error) {
	modelsResponse, err := makeModelsRequest(url, bearerKey)
	if err != nil {
		return nil, err
	}
	response := []string{}

	for _, v := range modelsResponse.Data {
		response = append(response, v.ID)
	}
	return response, nil
}

func API(appConfig *config.ApplicationConfig) (*fiber.App, error) {

	fiberCfg := fiber.Config{
		Views:     laihttp.RenderEngine(),
		BodyLimit: appConfig.UploadLimitMB * 1024 * 1024, // this is the default limit of 4MB
		// We disable the Fiber startup message as it does not conform to structured logging.
		// We register a startup log line with connection information in the OnListen hook to keep things user friendly though
		DisableStartupMessage: true,
		// Override default error handler
	}

	// Normally, return errors as JSON responses
	fiberCfg.ErrorHandler = func(ctx *fiber.Ctx, err error) error {
		// Status code defaults to 500
		code := fiber.StatusInternalServerError

		// Retrieve the custom status code if it's a *fiber.Error
		var e *fiber.Error
		if errors.As(err, &e) {
			code = e.Code
		}

		// Send custom error page
		return ctx.Status(code).JSON(
			schema.ErrorResponse{
				Error: &schema.APIError{Message: err.Error(), Code: code},
			},
		)
	}

	router := fiber.New(fiberCfg)

	router.Use(middleware.StripPathPrefix())

	router.Hooks().OnListen(func(listenData fiber.ListenData) error {
		scheme := "http"
		if listenData.TLS {
			scheme = "https"
		}
		log.Info().Str("endpoint", scheme+"://"+listenData.Host+":"+listenData.Port).Msg("LocalAI API is listening! Please connect to the endpoint for API documentation.")
		return nil
	})

	// Have Fiber use zerolog like the rest of the application rather than it's built-in logger
	logger := log.Logger
	router.Use(fiberzerolog.New(fiberzerolog.Config{
		Logger: &logger,
	}))

	// Default middleware config

	if !appConfig.Debug {
		router.Use(recover.New())
	}

	// Load config jsons
	utils.LoadConfig(appConfig.UploadDir, openai.UploadedFilesFile, &openai.UploadedFiles)
	utils.LoadConfig(appConfig.ConfigsDir, openai.AssistantsConfigFile, &openai.Assistants)
	utils.LoadConfig(appConfig.ConfigsDir, openai.AssistantsFileConfigFile, &openai.AssistantFiles)

	router.Get("/", func(c *fiber.Ctx) error {
		models, err := getModelsConfigs("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		summary := fiber.Map{
			"Title":             "LocalAI API - " + internal.PrintableVersion(),
			"Version":           internal.PrintableVersion(),
			"BaseURL":           laihttputils.BaseURL(c),
			"Models":            models,
			"ModelsConfig":      []config.BackendConfig{},
			"GalleryConfig":     map[string]*gallery.Config{},
			"IsP2PEnabled":      p2p.IsP2PEnabled(),
			"ApplicationConfig": appConfig,
			"ProcessingModels":  map[string]string{},
			"TaskTypes":         map[string]string{},
		}

		if string(c.Context().Request.Header.ContentType()) == "application/json" || len(c.Accepts("html")) == 0 {
			// The client expects a JSON response
			return c.Status(fiber.StatusOK).JSON(summary)
		} else {
			// Render index
			return c.Render("views/index", summary)
		}
	})

	// Show the Chat page
	router.Get("/chat/:model", func(c *fiber.Ctx) error {
		models, err := getModels("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Chat with " + c.Params("model"),
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": models,
			"Model":        c.Params("model"),
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/chat", summary)
	})

	router.Get("/talk/", func(c *fiber.Ctx) error {
		models, err := getModels("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Talk",
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": models,
			"Model":        models[0],
			"IsP2PEnabled": p2p.IsP2PEnabled(),
			"Version":      internal.PrintableVersion(),
		}

		// Render index
		return c.Render("views/talk", summary)
	})

	router.Get("/chat/", func(c *fiber.Ctx) error {
		models, err := getModels("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Chat with " + models[0],
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": models,
			"Model":        models[0],
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/chat", summary)
	})

	router.Get("/text2image/:model", func(c *fiber.Ctx) error {
		models, err := getModelsConfigs("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + c.Params("model"),
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": models,
			"Model":        c.Params("model"),
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/text2image", summary)
	})

	router.Get("/text2image/", func(c *fiber.Ctx) error {
		models, err := getModelsConfigs("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + models[0].Name,
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": models,
			"Model":        models[0].Name,
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/text2image", summary)
	})

	router.Get("/tts/:model", func(c *fiber.Ctx) error {
		models, err := getModelsConfigs("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + c.Params("model"),
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": models,
			"Model":        c.Params("model"),
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/tts", summary)
	})

	router.Get("/tts/", func(c *fiber.Ctx) error {
		models, err := getModelsConfigs("http://127.0.0.1:8080/v1/models", "")
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate audio with " + models[0].Name,
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": models,
			"Model":        models[0].Name,
			"IsP2PEnabled": p2p.IsP2PEnabled(),
			"Version":      internal.PrintableVersion(),
		}

		// Render index
		return c.Render("views/tts", summary)
	})

	httpFS := http.FS(laihttp.EmbedDirStatic)

	router.Use(favicon.New(favicon.Config{
		URL:        "/favicon.ico",
		FileSystem: httpFS,
		File:       "static/favicon.ico",
	}))

	router.Use("/static", filesystem.New(filesystem.Config{
		Root:       httpFS,
		PathPrefix: "static",
		Browse:     true,
	}))

	// Define a custom 404 handler
	// Note: keep this at the bottom!
	router.Use(laihttp.NotFoundHandler)

	return router, nil
}
