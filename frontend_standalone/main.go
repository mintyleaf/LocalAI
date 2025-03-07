package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mudler/LocalAI/pkg/utils"

	"github.com/mudler/LocalAI/core/config"
	"github.com/mudler/LocalAI/core/gallery"
	laihttp "github.com/mudler/LocalAI/core/http"
	"github.com/mudler/LocalAI/core/http/endpoints/openai"
	"github.com/mudler/LocalAI/core/http/middleware"

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

type Me struct {
	Username string `json:"username"`
	Usage    Usage  `json:"usage"`
	Token    string `json:"token"`
	Reason   string `json:"reason"`
	Level    int    `json:"level"`
}

type Usage struct {
	Total        int `json:"total"`
	Completion   int `json:"completion"`
	Prompt       int `json:"prompt"`
	Limit        int `json:"limit"`
	BurnedTokens int `json:"burned_tokens"`
}

type Machines struct {
	Machines      map[string]MachineUsage `json:"machine_usage"`
	TokensTotal   int                     `json:"tokens_total"`
	WorktimeTotal float64                 `json:"worktime_total"`
}

type MachineUsage struct {
	TokensTotal      int     `json:"tokens_total"`
	TokensCompletion int     `json:"tokens_completion"`
	TokensPrompt     int     `json:"tokens_prompt"`
	TimingPrompt     float64 `json:"timing_prompt"`
	TimingCompletion float64 `json:"timing_completion"`
}

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

func makeModelsRequest(c *fiber.Ctx, apiURL string) (schema.ModelsDataResponse, error) {
	modelsResponse := schema.ModelsDataResponse{}

	agent := fiber.AcquireAgent()
	agent.Request().Header.SetMethod("GET")
	agent.Request().Header.SetContentType("application/json")
	agent.Request().SetRequestURI(apiURL + "/v1/models")
	agent.Request().Header.SetCookie("jwt", c.Cookies("jwt"))
	agent.Request().Header.SetCookie("LocalAI-Head", c.Cookies("LocalAI-Head"))
	err := agent.Parse()
	if err != nil {
		return modelsResponse, err
	}
	_, body, errs := agent.Bytes()
	if len(errs) > 0 {
		return modelsResponse, errs[0]
	}

	err = json.Unmarshal(body, &modelsResponse)
	if err != nil {
		return modelsResponse, err
	}
	return modelsResponse, nil
}

func getModelsConfigs(c *fiber.Ctx, apiURL string) ([]config.BackendConfig, error) {
	modelsResponse, err := makeModelsRequest(c, apiURL)
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

func getModels(c *fiber.Ctx, apiURL string) ([]string, error) {
	modelsResponse, err := makeModelsRequest(c, apiURL)
	if err != nil {
		return nil, err
	}
	response := []string{}

	for _, v := range modelsResponse.Data {
		response = append(response, v.ID)
	}
	return response, nil
}

func getModelsMeta(c *fiber.Ctx, apiURL, head string) ([]*gallery.GalleryModel, error) {
	modelsResponse := []*gallery.GalleryModel{}

	agent := fiber.AcquireAgent()
	agent.Request().Header.SetMethod("GET")
	agent.Request().Header.SetContentType("application/json")
	agent.Request().SetRequestURI(apiURL + "/lai/" + head + "/models/available")
	agent.Request().Header.SetCookie("jwt", c.Cookies("jwt"))
	agent.Request().Header.SetCookie("LocalAI-Head", c.Cookies("LocalAI-Head"))
	err := agent.Parse()
	if err != nil {
		return modelsResponse, err
	}
	_, body, errs := agent.Bytes()
	if len(errs) > 0 {
		return modelsResponse, errs[0]
	}

	err = json.Unmarshal(body, &modelsResponse)
	if err != nil {
		return modelsResponse, err
	}
	return modelsResponse, nil

}

func round(num float64) int {
	return int(num + math.Copysign(0.5, num))
}

func toFixed(num float64, precision int) float64 {
	output := math.Pow(10, float64(precision))
	return float64(round(num*output)) / output
}

func getMachines(c *fiber.Ctx, apiURL string) (Machines, error) {
	agent := fiber.AcquireAgent()
	agent.Request().Header.SetMethod("GET")
	agent.Request().SetRequestURI(apiURL + "/machines")
	agent.Request().Header.SetCookie("jwt", c.Cookies("jwt"))
	err := agent.Parse()
	var machinesResponse Machines
	if err != nil {
		return machinesResponse, err
	}
	statusCode, body, errs := agent.Bytes()
	if len(errs) > 0 {
		return machinesResponse, errs[0]
	}
	if statusCode != http.StatusOK {
		return machinesResponse, fmt.Errorf("Non 200 OK status code: %d", statusCode)
	}
	err = json.Unmarshal(body, &machinesResponse)
	if err != nil {
		return machinesResponse, err
	}

	machinesResponse.WorktimeTotal = toFixed(machinesResponse.WorktimeTotal*0.001, 1)
	for k, v := range machinesResponse.Machines {
		v.TimingCompletion = toFixed(v.TimingCompletion*0.001, 1)
		v.TimingPrompt = toFixed(v.TimingPrompt*0.001, 1)
		machinesResponse.Machines[k] = v
	}

	return machinesResponse, nil
}

func getAddress(c *fiber.Ctx, apiURL string) (string, error) {
	agent := fiber.AcquireAgent()
	agent.Request().Header.SetMethod("GET")
	agent.Request().SetRequestURI(apiURL + "/address")
	agent.Request().Header.SetCookie("jwt", c.Cookies("jwt"))
	err := agent.Parse()
	if err != nil {
		return "", err
	}
	statusCode, body, errs := agent.Bytes()
	if len(errs) > 0 {
		return "", errs[0]
	}
	if statusCode != http.StatusOK {
		return "", fmt.Errorf("Non 200 OK status code: %d", statusCode)
	}
	type AddressResponse struct {
		Address string `json:"address"`
	}

	address := AddressResponse{}
	err = json.Unmarshal(body, &address)
	if err != nil {
		return "", err
	}

	return address.Address, err
}

func getMe(c *fiber.Ctx, apiURL string) (Me, error) {
	agent := fiber.AcquireAgent()
	agent.Request().Header.SetMethod("GET")
	agent.Request().Header.SetContentType("application/json")
	agent.Request().SetRequestURI(apiURL + "/me")
	agent.Request().Header.SetCookie("jwt", c.Cookies("jwt"))
	err := agent.Parse()
	meResponse := Me{}
	if err != nil {
		return meResponse, err
	}
	statusCode, body, errs := agent.Bytes()
	if len(errs) > 0 {
		return meResponse, errs[0]
	}
	if statusCode != http.StatusOK {
		return meResponse, fmt.Errorf("Non 200 OK status code: %d", statusCode)
	}

	err = json.Unmarshal(body, &meResponse)
	if err != nil {
		return meResponse, err
	}
	return meResponse, nil
}

func getHeads(c *fiber.Ctx, apiURL, cookieDomain string) ([]string, string, error) {
	agent := fiber.AcquireAgent()
	agent.Request().Header.SetMethod("GET")
	agent.Request().Header.SetContentType("application/json")
	agent.Request().SetRequestURI(apiURL + "/heads")
	agent.Request().Header.SetCookie("jwt", c.Cookies("jwt"))
	err := agent.Parse()
	if err != nil {
		return nil, "", err
	}
	statusCode, body, errs := agent.Bytes()
	if len(errs) > 0 {
		return nil, "", errs[0]
	}
	if statusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Non 200 OK status code: %d", statusCode)
	}
	type HeadsResponse struct {
		Heads []string `json:"heads"`
	}

	heads := HeadsResponse{}
	err = json.Unmarshal(body, &heads)
	if err != nil {
		return nil, "", err
	}

	head := c.Cookies("LocalAI-Head", "")
	if head == "" {
		head = heads.Heads[0]
		c.Cookie(&fiber.Cookie{
			Name:   "LocalAI-Head",
			Value:  head,
			Path:   "/",
			Domain: cookieDomain,
			Secure: os.Getenv("COOKIE_INSECURE") == "",
		})
	}

	return heads.Heads, head, nil
}

func logout(c *fiber.Ctx, apiURL, cookieDomain string) error {
	agent := fiber.AcquireAgent()
	agent.Request().Header.SetMethod("POST")
	agent.Request().SetRequestURI(apiURL + "/logout")
	agent.Request().Header.SetCookie("jwt", c.Cookies("jwt"))

	c.Cookie(&fiber.Cookie{
		Name:    "jwt",
		Value:   "",
		Path:    "/",
		Domain:  cookieDomain,
		Secure:  os.Getenv("COOKIE_INSECURE") == "",
		Expires: time.Now().AddDate(-1, 0, 0),
	})

	err := agent.Parse()
	if err != nil {
		return err
	}
	statusCode, _, errs := agent.Bytes()
	if len(errs) > 0 {
		return errs[0]
	}
	if statusCode != http.StatusOK {
		return fmt.Errorf("Non 200 OK status code: %d", statusCode)
	}

	return nil
}

func API(appConfig *config.ApplicationConfig) (*fiber.App, error) {

	contractAddress := os.Getenv("CONTRACT_ADDRESS")
	contractABI := os.Getenv("CONTRACT_ABI")

	listenURL := os.Getenv("LISTEN_URL")
	apiURL := os.Getenv("API_URL")
	cookieDomain := os.Getenv("COOKIE_DOMAIN")

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
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, _ := getMe(c, apiURL)
		machines, _ := getMachines(c, apiURL)

		summary := fiber.Map{
			"BaseURL":  listenURL,
			"APIURL":   apiURL,
			"Username": me.Username,
			"Usage":    me.Usage,
			"Token":    me.Token,
			"Balance":  me.Usage.Limit - me.Usage.Total,
			"Reason":   me.Reason,
			"Heads":    heads,
			"Head":     head,
			"ToBurn":   machines.TokensTotal - me.Usage.BurnedTokens,
			"Machines": machines,
		}

		if string(c.Context().Request.Header.ContentType()) == "application/json" || len(c.Accepts("html")) == 0 {
			// The client expects a JSON response
			return c.Status(fiber.StatusOK).JSON(summary)
		} else {
			// Render index
			return c.Render("views/standalone_index", summary)
		}
	})

	router.Get("/logout", func(c *fiber.Ctx) error {
		_ = logout(c, apiURL, cookieDomain)
		return c.Redirect(listenURL)
	})

	router.Get("/settings", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, _ := getMe(c, apiURL)
		machines, _ := getMachines(c, apiURL)

		summary := fiber.Map{
			"BaseURL":         listenURL,
			"APIURL":          apiURL,
			"Username":        me.Username,
			"Usage":           me.Usage,
			"Token":           me.Token,
			"Balance":         me.Usage.Limit - me.Usage.Total,
			"Reason":          me.Reason,
			"Heads":           heads,
			"Head":            head,
			"ContractABI":     contractABI,
			"ContractAddress": contractAddress,
			"ToBurn":          machines.TokensTotal - me.Usage.BurnedTokens,
			"Machines":        machines,
		}

		if string(c.Context().Request.Header.ContentType()) == "application/json" || len(c.Accepts("html")) == 0 {
			// The client expects a JSON response
			return c.Status(fiber.StatusOK).JSON(summary)
		} else {
			// Render index
			return c.Render("views/standalone_settings", summary)
		}
	})

	router.Get("/browse", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		models, _ := getModels(c, apiURL)
		modelsMeta, _ := getModelsMeta(c, apiURL, head)
		filteredModels := []*gallery.GalleryModel{}
		for _, meta := range modelsMeta {
			indexMatch := -1
			for idx, name := range models {
				// TODO do exact match, that is done only for demo purposes because of stablediffusion naming
				if strings.HasPrefix(meta.Name, name) {
					indexMatch = idx
					break
				}
			}
			if indexMatch < 0 {
				continue
			}
			models = append(models[:indexMatch], models[indexMatch+1:]...)
			filteredModels = append(filteredModels, meta)
		}
		for _, name := range models {
			emptyModel := gallery.GalleryModel{
				Name: name,
			}
			filteredModels = append(filteredModels, &emptyModel)
		}

		me, _ := getMe(c, apiURL)

		summary := fiber.Map{
			"BaseURL":  listenURL,
			"APIURL":   apiURL,
			"Username": me.Username,
			"Usage":    me.Usage,
			"Token":    me.Token,
			"Balance":  me.Usage.Limit - me.Usage.Total,
			"Reason":   me.Reason,
			"Heads":    heads,
			"Head":     head,
			"Models":   filteredModels,
		}

		if string(c.Context().Request.Header.ContentType()) == "application/json" || len(c.Accepts("html")) == 0 {
			// The client expects a JSON response
			return c.Status(fiber.StatusOK).JSON(summary)
		} else {
			// Render index
			return c.Render("views/standalone_browse", summary)
		}
	})

	router.Get("/p2p", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, _ := getMe(c, apiURL)

		summary := fiber.Map{
			"BaseURL":  listenURL,
			"APIURL":   apiURL,
			"Username": me.Username,
			"Usage":    me.Usage,
			"Token":    me.Token,
			"Balance":  me.Usage.Limit - me.Usage.Total,
			"Reason":   me.Reason,
			"Heads":    heads,
			"Head":     head,
		}

		if string(c.Context().Request.Header.ContentType()) == "application/json" || len(c.Accepts("html")) == 0 {
			// The client expects a JSON response
			return c.Status(fiber.StatusOK).JSON(summary)
		} else {
			// Render index
			return c.Render("views/standalone_p2p", summary)
		}
	})

	// Show the Chat page
	router.Get("/chat/:model", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, err := getMe(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getMe")
		}
		models, err := getModels(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(listenURL)
		}

		if !slices.Contains(models, c.Params("model")) {
			return c.Redirect(listenURL + "/chat")
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Chat with " + c.Params("model"),
			"BaseURL":      listenURL,
			"APIURL":       apiURL,
			"ModelsConfig": models,
			"Model":        c.Params("model"),
			"Username":     me.Username,
			"Usage":        me.Usage,
			"Balance":      me.Usage.Limit - me.Usage.Total,
			"Reason":       me.Reason,
			"Heads":        heads,
			"Head":         head,
		}

		// Render index
		return c.Render("views/chat", summary)
	})

	router.Get("/talk/", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, err := getMe(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getMe")
		}
		models, err := getModels(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(listenURL)
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Talk",
			"BaseURL":      listenURL,
			"APIURL":       apiURL,
			"ModelsConfig": models,
			"Model":        models[0],
			"Username":     me.Username,
			"Usage":        me.Usage,
			"Balance":      me.Usage.Limit - me.Usage.Total,
			"Reason":       me.Reason,
			"Heads":        heads,
			"Head":         head,
		}

		// Render index
		return c.Render("views/talk", summary)
	})

	router.Get("/chat/", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, err := getMe(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getMe")
		}
		models, err := getModels(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(listenURL)
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Chat with " + models[0],
			"BaseURL":      listenURL,
			"APIURL":       apiURL,
			"ModelsConfig": models,
			"Model":        models[0],
			"Username":     me.Username,
			"Usage":        me.Usage,
			"Balance":      me.Usage.Limit - me.Usage.Total,
			"Reason":       me.Reason,
			"Heads":        heads,
			"Head":         head,
		}

		// Render index
		return c.Render("views/chat", summary)
	})

	router.Get("/text2image/:model", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, err := getMe(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getMe")
		}
		models, err := getModels(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + c.Params("model"),
			"BaseURL":      listenURL,
			"APIURL":       apiURL,
			"ModelsConfig": models,
			"Model":        c.Params("model"),
			"Username":     me.Username,
			"Usage":        me.Usage,
			"Balance":      me.Usage.Limit - me.Usage.Total,
			"Reason":       me.Reason,
			"Heads":        heads,
			"Head":         head,
		}

		// Render index
		return c.Render("views/text2image", summary)
	})

	router.Get("/text2image/", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, err := getMe(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getMe")
		}
		models, err := getModels(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(listenURL)
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + models[0],
			"BaseURL":      listenURL,
			"APIURL":       apiURL,
			"ModelsConfig": models,
			"Model":        models[0],
			"Username":     me.Username,
			"Usage":        me.Usage,
			"Balance":      me.Usage.Limit - me.Usage.Total,
			"Reason":       me.Reason,
			"Heads":        heads,
			"Head":         head,
		}

		// Render index
		return c.Render("views/text2image", summary)
	})

	router.Get("/tts/:model", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, err := getMe(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getMe")
		}
		models, err := getModels(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate images with " + c.Params("model"),
			"BaseURL":      listenURL,
			"APIURL":       apiURL,
			"ModelsConfig": models,
			"Model":        c.Params("model"),
			"Username":     me.Username,
			"Usage":        me.Usage,
			"Balance":      me.Usage.Limit - me.Usage.Total,
			"Reason":       me.Reason,
			"Heads":        heads,
			"Head":         head,
		}

		// Render index
		return c.Render("views/tts", summary)
	})

	router.Get("/tts/", func(c *fiber.Ctx) error {
		heads, head, err := getHeads(c, apiURL, cookieDomain)
		if err != nil {
			log.Error().Err(err).Msg("getHeads")
		}
		me, err := getMe(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getMe")
		}
		models, err := getModels(c, apiURL)
		if err != nil {
			log.Error().Err(err).Msg("getModels")
		}

		if len(models) == 0 {
			// If no model is available redirect to the index which suggests how to install models
			return c.Redirect(listenURL)
		}

		summary := fiber.Map{
			"Title":        "LocalAI - Generate audio with " + models[0],
			"BaseURL":      listenURL,
			"APIURL":       apiURL,
			"ModelsConfig": models,
			"Model":        models[0],
			"Username":     me.Username,
			"Usage":        me.Usage,
			"Balance":      me.Usage.Limit - me.Usage.Total,
			"Reason":       me.Reason,
			"Heads":        heads,
			"Head":         head,
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
