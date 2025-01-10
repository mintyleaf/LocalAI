package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/dave-gray101/v2keyauth"
	"github.com/mudler/LocalAI/internal"
	"github.com/mudler/LocalAI/pkg/model"
	"github.com/mudler/LocalAI/pkg/utils"

	"github.com/mudler/LocalAI/core/config"
	"github.com/mudler/LocalAI/core/gallery"
	laihttp "github.com/mudler/LocalAI/core/http"
	"github.com/mudler/LocalAI/core/http/endpoints/localai"
	"github.com/mudler/LocalAI/core/http/endpoints/openai"
	"github.com/mudler/LocalAI/core/http/middleware"
	"github.com/mudler/LocalAI/core/http/routes"
	laihttputils "github.com/mudler/LocalAI/core/http/utils"
	"github.com/mudler/LocalAI/core/p2p"

	"github.com/mudler/LocalAI/core/schema"
	"github.com/mudler/LocalAI/core/services"

	"github.com/gofiber/contrib/fiberzerolog"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/csrf"
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

	if err := appHTTP.Listen(":8080"); err != nil {
		log.Error().Err(err).Msg("error during HTTP App construction")
		return
	}

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

	if !appConfig.OpaqueErrors {
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
	} else {
		// If OpaqueErrors are required, replace everything with a blank 500.
		fiberCfg.ErrorHandler = func(ctx *fiber.Ctx, _ error) error {
			return ctx.Status(500).SendString("")
		}
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

	if !appConfig.DisableMetrics {
		metricsService, err := services.NewLocalAIMetricsService()
		if err != nil {
			return nil, err
		}

		if metricsService != nil {
			router.Use(localai.LocalAIMetricsAPIMiddleware(metricsService))
			router.Hooks().OnShutdown(func() error {
				return metricsService.Shutdown()
			})
		}

	}
	// Health Checks should always be exempt from auth, so register these first
	routes.HealthRoutes(router)

	kaConfig, err := middleware.GetKeyAuthConfig(appConfig)
	if err != nil || kaConfig == nil {
		return nil, fmt.Errorf("failed to create key auth config: %w", err)
	}

	// Auth is applied to _all_ endpoints. No exceptions. Filtering out endpoints to bypass is the role of the Filter property of the KeyAuth Configuration
	router.Use(v2keyauth.New(*kaConfig))

	if appConfig.CORS {
		var c func(ctx *fiber.Ctx) error
		if appConfig.CORSAllowOrigins == "" {
			c = cors.New()
		} else {
			c = cors.New(cors.Config{AllowOrigins: appConfig.CORSAllowOrigins})
		}

		router.Use(c)
	}

	if appConfig.CSRF {
		log.Debug().Msg("Enabling CSRF middleware. Tokens are now required for state-modifying requests")
		router.Use(csrf.New())
	}

	// Load config jsons
	utils.LoadConfig(appConfig.UploadDir, openai.UploadedFilesFile, &openai.UploadedFiles)
	utils.LoadConfig(appConfig.ConfigsDir, openai.AssistantsConfigFile, &openai.Assistants)
	utils.LoadConfig(appConfig.ConfigsDir, openai.AssistantsFileConfigFile, &openai.AssistantFiles)

	cl := &config.BackendConfigLoader{}
	ml := &model.ModelLoader{}
	modelStatus := func() (map[string]string, map[string]string) {
		return nil, nil
	}

	router.Get("/", func(c *fiber.Ctx) error {
		backendConfigs := cl.GetAllBackendConfigs()
		galleryConfigs := map[string]*gallery.Config{}

		for _, m := range backendConfigs {
			cfg, err := gallery.GetLocalModelConfiguration(ml.ModelPath, m.Name)
			if err != nil {
				continue
			}
			galleryConfigs[m.Name] = cfg
		}

		modelsWithoutConfig, _ := services.ListModels(cl, ml, config.NoFilterFn, services.LOOSE_ONLY)

		// Get model statuses to display in the UI the operation in progress
		processingModels, taskTypes := modelStatus()

		summary := fiber.Map{
			"Title":             "LocalAI API - " + internal.PrintableVersion(),
			"Version":           internal.PrintableVersion(),
			"BaseURL":           laihttputils.BaseURL(c),
			"Models":            modelsWithoutConfig,
			"ModelsConfig":      backendConfigs,
			"GalleryConfig":     galleryConfigs,
			"IsP2PEnabled":      p2p.IsP2PEnabled(),
			"ApplicationConfig": appConfig,
			"ProcessingModels":  processingModels,
			"TaskTypes":         taskTypes,
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
		backendConfigs, _ := services.ListModels(cl, ml, config.NoFilterFn, services.SKIP_IF_CONFIGURED)

		summary := fiber.Map{
			"Title":        "LocalAI - Chat with " + c.Params("model"),
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": backendConfigs,
			"Model":        c.Params("model"),
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/chat", summary)
	})

	router.Get("/talk/", func(c *fiber.Ctx) error {
		backendConfigs, _ := services.ListModels(cl, ml, config.NoFilterFn, services.SKIP_IF_CONFIGURED)

		if len(backendConfigs) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Talk",
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": backendConfigs,
			"Model":        backendConfigs[0],
			"IsP2PEnabled": p2p.IsP2PEnabled(),
			"Version":      internal.PrintableVersion(),
		}

		// Render index
		return c.Render("views/talk", summary)
	})

	router.Get("/chat/", func(c *fiber.Ctx) error {

		backendConfigs, _ := services.ListModels(cl, ml, config.NoFilterFn, services.SKIP_IF_CONFIGURED)

		if len(backendConfigs) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Chat with " + backendConfigs[0],
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": backendConfigs,
			"Model":        backendConfigs[0],
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/chat", summary)
	})

	router.Get("/text2image/:model", func(c *fiber.Ctx) error {
		backendConfigs := cl.GetAllBackendConfigs()

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + c.Params("model"),
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": backendConfigs,
			"Model":        c.Params("model"),
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/text2image", summary)
	})

	router.Get("/text2image/", func(c *fiber.Ctx) error {

		backendConfigs := cl.GetAllBackendConfigs()

		if len(backendConfigs) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + backendConfigs[0].Name,
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": backendConfigs,
			"Model":        backendConfigs[0].Name,
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/text2image", summary)
	})

	router.Get("/tts/:model", func(c *fiber.Ctx) error {
		backendConfigs := cl.GetAllBackendConfigs()

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + c.Params("model"),
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": backendConfigs,
			"Model":        c.Params("model"),
			"Version":      internal.PrintableVersion(),
			"IsP2PEnabled": p2p.IsP2PEnabled(),
		}

		// Render index
		return c.Render("views/tts", summary)
	})

	router.Get("/tts/", func(c *fiber.Ctx) error {

		backendConfigs := cl.GetAllBackendConfigs()

		if len(backendConfigs) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(laihttputils.BaseURL(c))
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate audio with " + backendConfigs[0].Name,
			"BaseURL":      laihttputils.BaseURL(c),
			"ModelsConfig": backendConfigs,
			"Model":        backendConfigs[0].Name,
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
